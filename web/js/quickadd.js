// Quick add parser: reads a date, a time or time range, a duration, a
// recurrence and a location out of a one-line event, in English or French.
// Dates and recurrences follow the same rules as Get Done's quick add.
//
//   "Sport demain 18h-19h30 @Gym"   -> tomorrow 18:00-19:30, location "Gym"
//   "Deep work lundi 9h pour 2h"    -> next Monday 09:00-11:00
//   "Holidays 24 dec"               -> all day on 24 December
//   "Standup every weekday 9:30"    -> 09:30-10:30, Monday to Friday

import { addDays, atMinutes, dayLabel, hhmm, isoDate, nextWeekday, parseISO, rruleLabel, todayISO } from './dates.js';

const MONTH_PATTERN = 'january|janvier|janv|jan|february|f[eé]vrier|f[eé]vr|f[eé]v|feb|march|mars|mar|april|avril|avr|apr|may|mai|june|juin|jun|july|juillet|juil|jul|august|ao[uû]t|aug|september|septembre|sept|sep|october|octobre|oct|november|novembre|nov|december|d[eé]cembre|d[eé]c|dec';

const MONTH_NUMBERS = {
  january: 1, janvier: 1, janv: 1, jan: 1,
  february: 2, fevrier: 2, fevr: 2, fev: 2, feb: 2,
  march: 3, mars: 3, mar: 3,
  april: 4, avril: 4, avr: 4, apr: 4,
  may: 5, mai: 5,
  june: 6, juin: 6, jun: 6,
  july: 7, juillet: 7, juil: 7, jul: 7,
  august: 8, aout: 8, aug: 8,
  september: 9, septembre: 9, sept: 9, sep: 9,
  october: 10, octobre: 10, oct: 10,
  november: 11, novembre: 11, nov: 11,
  december: 12, decembre: 12, dec: 12,
};

// A clock time: "14h", "14h30", "14:30", "2pm", "2:30 pm".
const CLOCK = String.raw`(\d{1,2})(?:(?::(\d{2}))?\s*(am|pm)|h(\d{2})?|:(\d{2}))`;
// Same, but the first end of a range may omit the unit: "14-16h".
const CLOCK_LOOSE = String.raw`(\d{1,2})(?:(?::(\d{2}))?\s*(am|pm)|h(\d{2})?|:(\d{2}))?`;
// \b only knows ASCII letters, so words like "à" are delimited by spaces.
const RANGE_RE = new RegExp(String.raw`(?:(?:^|\s)(?:de|from)\s+)?\b${CLOCK_LOOSE}(?:\s*[-–]\s*|\s+(?:à|a|to|until|jusqu'à)\s+)${CLOCK}\b`, 'i');
const TIME_RE = new RegExp(String.raw`(?:(?:^|\s)(?:à|at)\s+)?\b${CLOCK}\b`, 'i');
const DURATION_RE = /\b(?:pour|for|during|durant)\s+(\d{1,2})\s*(h|min|mn|m)\s*(\d{2})?\b|\b(\d{1,3})\s*(?:min|mn|minutes)\b/i;
const ISO_RE = /\b(\d{4})-(\d{2})-(\d{2})\b/;
const DAY_MONTH_RE = new RegExp(`\\b(\\d{1,2})(?:st|nd|rd|th|er)?\\s+(${MONTH_PATTERN})\\.?(?:\\s+(\\d{4}))?\\b`, 'i');
const MONTH_DAY_RE = new RegExp(`\\b(${MONTH_PATTERN})\\.?\\s+(\\d{1,2})(?:st|nd|rd|th|er)?(?:,?\\s+(\\d{4}))?\\b`, 'i');
const NUMERIC_RE = /\b(\d{1,2})\/(\d{1,2})(?:\/(\d{2,4}))?\b/;

const RECURRENCES = [
  [/\b(?:tous les jours ouvrés|jours ouvrés|every weekday|weekdays)\b/i, 'FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR'],
  [/\b(?:tous les jours|chaque jour|every day|daily)\b/i, 'FREQ=DAILY'],
  [/\b(?:toutes les semaines|chaque semaine|every week|weekly)\b/i, 'FREQ=WEEKLY'],
  [/\b(?:tous les mois|chaque mois|every month|monthly)\b/i, 'FREQ=MONTHLY'],
  [/\b(?:tous les ans|chaque année|chaque annee|every year|yearly)\b/i, 'FREQ=YEARLY'],
  [/\b(?:tous les lundis|chaque lundi|every monday)\b/i, 'FREQ=WEEKLY;BYDAY=MO', 1],
  [/\b(?:tous les mardis|chaque mardi|every tuesday)\b/i, 'FREQ=WEEKLY;BYDAY=TU', 2],
  [/\b(?:tous les mercredis|chaque mercredi|every wednesday)\b/i, 'FREQ=WEEKLY;BYDAY=WE', 3],
  [/\b(?:tous les jeudis|chaque jeudi|every thursday)\b/i, 'FREQ=WEEKLY;BYDAY=TH', 4],
  [/\b(?:tous les vendredis|chaque vendredi|every friday)\b/i, 'FREQ=WEEKLY;BYDAY=FR', 5],
  [/\b(?:tous les samedis|chaque samedi|every saturday)\b/i, 'FREQ=WEEKLY;BYDAY=SA', 6],
  [/\b(?:tous les dimanches|chaque dimanche|every sunday)\b/i, 'FREQ=WEEKLY;BYDAY=SU', 0],
];

const RELATIVE_DAYS = [
  [/\b(?:après-demain|apres-demain|day after tomorrow)\b/i, 2],
  [/\b(?:aujourd'hui|aujourdhui|today|ajd)\b/i, 0],
  [/\b(?:demain|tomorrow)\b/i, 1],
];

// "mon" is left out on purpose: in French it means "my".
const WEEKDAY_WORDS = [
  [/\b(?:lundi|monday)\b/i, 1],
  [/\b(?:mardi|tuesday|tue)\b/i, 2],
  [/\b(?:mercredi|wednesday|wed)\b/i, 3],
  [/\b(?:jeudi|thursday|thu)\b/i, 4],
  [/\b(?:vendredi|friday|fri)\b/i, 5],
  [/\b(?:samedi|saturday|sat)\b/i, 6],
  [/\b(?:dimanche|sunday|sun)\b/i, 0],
];

function monthNumber(name) {
  return MONTH_NUMBERS[name.toLowerCase().replace(/é/g, 'e').replace(/û/g, 'u')] || 0;
}

/** "YYYY-MM-DD" for a valid calendar date, else null. */
function makeDate(year, month, day) {
  const d = new Date(year, month - 1, day);
  return d.getFullYear() === year && d.getMonth() === month - 1 && d.getDate() === day ? isoDate(d) : null;
}

/** Day + month (+ optional year); without year, a past date means next year. */
function calendarDate(day, month, yearStr, today) {
  let year = parseISO(today).getFullYear();
  if (yearStr) {
    year = Number(yearStr);
    if (year < 100) year += 2000;
  }
  let iso = makeDate(year, month, Number(day));
  if (iso && !yearStr && iso < today) iso = makeDate(year + 1, month, Number(day)) || iso;
  return iso;
}

/** Minutes after midnight from the groups of CLOCK, or null. */
function clockMinutes(hour, minA, ampm, minB, minC) {
  let h = Number(hour);
  const m = Number(minA || minB || minC || 0);
  const suffix = (ampm || '').toLowerCase();
  if (suffix === 'pm' && h < 12) h += 12;
  if (suffix === 'am' && h === 12) h = 0;
  return h <= 23 && m <= 59 ? h * 60 + m : null;
}

/** Removes a regex match from the string it was found in. */
function cut(work, match) {
  return `${work.slice(0, match.index)} ${work.slice(match.index + match[0].length)}`;
}

function findDate(work, today) {
  const attempts = [
    [ISO_RE, (m) => makeDate(Number(m[1]), Number(m[2]), Number(m[3]))],
    [DAY_MONTH_RE, (m) => calendarDate(m[1], monthNumber(m[2]), m[3], today)],
    [MONTH_DAY_RE, (m) => calendarDate(m[2], monthNumber(m[1]), m[3], today)],
    [NUMERIC_RE, (m) => calendarDate(m[1], Number(m[2]), m[3], today)],
    ...RELATIVE_DAYS.map(([re, offset]) => [re, () => addDays(today, offset)]),
    ...WEEKDAY_WORDS.map(([re, weekday]) => [re, () => nextWeekday(today, weekday)]),
  ];
  for (const [re, build] of attempts) {
    const m = re.exec(work);
    if (!m) continue;
    const iso = build(m);
    if (iso) return { date: iso, rest: cut(work, m) };
  }
  return null;
}

/**
 * Parses a one-line event. Returns
 *   { title, allDay, start: Date, end: Date, rrule, location, tokens }
 * where tokens describe what was recognized, for the preview chips.
 */
export function parseQuickAdd(text, { today = todayISO(), defaultMinutes = 60 } = {}) {
  let work = ` ${String(text || '')} `;
  const tokens = [];

  let rrule = '';
  let ruleWeekday = null;
  for (const [re, rule, weekday] of RECURRENCES) {
    const m = re.exec(work);
    if (m) {
      rrule = rule;
      ruleWeekday = weekday ?? null;
      work = cut(work, m);
      break;
    }
  }

  let startMin = null;
  let endMin = null;
  const range = RANGE_RE.exec(work);
  if (range) {
    const a = clockMinutes(range[1], range[2], range[3] || range[8], range[4], range[5]);
    const b = clockMinutes(range[6], range[7], range[8], range[9], range[10]);
    if (a !== null && b !== null) {
      startMin = a;
      endMin = b > a ? b : b + 24 * 60;
      work = cut(work, range);
    }
  }
  if (startMin === null) {
    const tm = TIME_RE.exec(work);
    if (tm) {
      const a = clockMinutes(tm[1], tm[2], tm[3], tm[4], tm[5]);
      if (a !== null) {
        startMin = a;
        work = cut(work, tm);
      }
    }
  }
  const dm = DURATION_RE.exec(work);
  if (dm) {
    let minutes;
    if (dm[4]) minutes = Number(dm[4]);
    else if (/^h$/i.test(dm[2])) minutes = Number(dm[1]) * 60 + Number(dm[3] || 0);
    else minutes = Number(dm[1]);
    if (minutes > 0) {
      if (startMin !== null) endMin = startMin + minutes;
      work = cut(work, dm);
    }
  }

  let date = null;
  const found = findDate(work, today);
  if (found) {
    date = found.date;
    work = found.rest;
  }
  // "every monday" without a date starts on the next Monday (or today).
  if (!date && ruleWeekday !== null) {
    date = parseISO(today).getDay() === ruleWeekday ? today : nextWeekday(today, ruleWeekday);
  }
  date = date || today;

  let location = '';
  const quoted = /@(["'])(.+?)\1/.exec(work);
  const plain = /@(\S.*)$/.exec(work);
  const lm = quoted || plain;
  if (lm) {
    location = (quoted ? quoted[2] : plain[1]).trim().replace(/\s+/g, ' ');
    work = cut(work, lm);
  }

  const allDay = startMin === null;
  let start;
  let end;
  if (allDay) {
    start = parseISO(date);
    end = parseISO(addDays(date, 1));
    tokens.push({ type: 'date', label: `${dayLabel(date)} · all day` });
  } else {
    start = atMinutes(date, startMin);
    end = atMinutes(date, endMin ?? startMin + defaultMinutes);
    tokens.push({ type: 'date', label: `${dayLabel(date)} · ${hhmm(start)} – ${hhmm(end)}` });
  }
  if (rrule) tokens.push({ type: 'recurrence', label: rruleLabel(rrule) });
  if (location) tokens.push({ type: 'location', label: location });

  return { title: work.trim().replace(/\s+/g, ' '), allDay, start, end, rrule, location, tokens };
}

