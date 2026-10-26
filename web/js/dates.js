// Calendar helpers. Days are "YYYY-MM-DD" strings in the browser's time zone;
// timed events carry Date objects. Never use toISOString() for days: it
// converts to UTC and shifts the date around midnight.

export const MONTHS = ['January', 'February', 'March', 'April', 'May', 'June',
  'July', 'August', 'September', 'October', 'November', 'December'];
export const MONTHS_SHORT = MONTHS.map((m) => m.slice(0, 3));
export const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
export const WEEKDAYS_SHORT = WEEKDAYS.map((d) => d.slice(0, 3));
export const RRULE_DAYS = ['SU', 'MO', 'TU', 'WE', 'TH', 'FR', 'SA'];

/** The browser's IANA time zone, sent with every write. */
export const TZ = Intl.DateTimeFormat().resolvedOptions().timeZone || '';

export const MINUTE = 60000;
export const DAY = 86400000;

export function pad2(n) {
  return String(n).padStart(2, '0');
}

/** Local "YYYY-MM-DD" of a Date. */
export function isoDate(d) {
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`;
}

export function todayISO() {
  return isoDate(new Date());
}

/** Date at local midnight for "YYYY-MM-DD", or null when invalid. */
export function parseISO(iso) {
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(iso || '');
  if (!m) return null;
  const d = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
  return d.getMonth() === Number(m[2]) - 1 ? d : null;
}

export function addDays(iso, days) {
  const d = parseISO(iso);
  d.setDate(d.getDate() + days);
  return isoDate(d);
}

export function addMonths(iso, months) {
  const d = parseISO(iso);
  const day = d.getDate();
  d.setDate(1);
  d.setMonth(d.getMonth() + months);
  d.setDate(Math.min(day, new Date(d.getFullYear(), d.getMonth() + 1, 0).getDate()));
  return isoDate(d);
}

/** Calendar days from a to b (both "YYYY-MM-DD"). */
export function daysBetween(a, b) {
  return Math.round((parseISO(b) - parseISO(a)) / DAY);
}

/** Monday of the week containing iso. */
export function weekStart(iso) {
  const d = parseISO(iso);
  return addDays(iso, -((d.getDay() + 6) % 7));
}

/** First day strictly after `iso` falling on `weekday` (0 = Sunday). */
export function nextWeekday(iso, weekday) {
  const d = parseISO(iso);
  let diff = weekday - d.getDay();
  if (diff <= 0) diff += 7;
  return addDays(iso, diff);
}

/** A Date at `minutes` after local midnight of day iso. */
export function atMinutes(iso, minutes) {
  const d = parseISO(iso);
  d.setMinutes(minutes);
  return d;
}

/** Minutes since local midnight. */
export function minutesOf(date) {
  return date.getHours() * 60 + date.getMinutes();
}

export function hhmm(date) {
  return `${pad2(date.getHours())}:${pad2(date.getMinutes())}`;
}

/** "14:00", or "14h" style compact label for grid hours. */
export function hourLabel(h) {
  return `${pad2(h)}:00`;
}

/** "Mon 26 Oct". */
export function dayLabel(iso) {
  const d = parseISO(iso);
  return `${WEEKDAYS_SHORT[d.getDay()]} ${d.getDate()} ${MONTHS_SHORT[d.getMonth()]}`;
}

/** "Monday 26 October 2026". */
export function longDayLabel(iso) {
  const d = parseISO(iso);
  return `${WEEKDAYS[d.getDay()]} ${d.getDate()} ${MONTHS[d.getMonth()]} ${d.getFullYear()}`;
}

/**
 * An occurrence from the API with parsed times:
 *   start, end: Date (all-day: local midnights, end exclusive)
 *   startDay, endDay: "YYYY-MM-DD" (endDay inclusive, the last day it covers)
 */
export function hydrate(e) {
  const start = e.all_day ? parseISO(e.start) : new Date(e.start);
  let end = e.all_day ? parseISO(e.end) : new Date(e.end);
  if (!(end > start)) end = e.all_day ? parseISO(addDays(e.start, 1)) : new Date(start.getTime());
  const lastMoment = new Date(Math.max(start.getTime(), end.getTime() - 1));
  return {
    ...e,
    key: `${e.calendar_id}/${e.name}/${e.recurrence_id || ''}`,
    start,
    end,
    startDay: isoDate(start),
    endDay: isoDate(lastMoment),
  };
}

/** Payload times for the API: days for all-day events, RFC 3339 otherwise. */
export function wireTimes(allDay, start, end) {
  if (allDay) return { start: isoDate(start), end: isoDate(end) };
  return { start: rfc3339(start), end: rfc3339(end) };
}

/** RFC 3339 with the local offset ("2026-10-26T14:00:00+01:00"). */
export function rfc3339(d) {
  const offset = -d.getTimezoneOffset();
  const sign = offset >= 0 ? '+' : '-';
  const abs = Math.abs(offset);
  return `${isoDate(d)}T${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}${sign}${pad2(Math.floor(abs / 60))}:${pad2(abs % 60)}`;
}

/** Time span label: "14:00 – 15:30", "All day", "26 Oct – 28 Oct". */
export function spanLabel(e) {
  if (e.all_day) {
    if (e.startDay === e.endDay) return 'All day';
    return `${dayLabel(e.startDay)} – ${dayLabel(e.endDay)}`;
  }
  if (e.startDay === e.endDay) return `${hhmm(e.start)} – ${hhmm(e.end)}`;
  return `${dayLabel(e.startDay)} ${hhmm(e.start)} – ${dayLabel(e.endDay)} ${hhmm(e.end)}`;
}

/** Human label of a recurrence rule written by this app, else "Custom". */
export function rruleLabel(rule) {
  if (!rule) return '';
  const parts = Object.fromEntries(rule.split(';').map((p) => p.split('=')));
  const names = { DAILY: 'Every day', WEEKLY: 'Every week', MONTHLY: 'Every month', YEARLY: 'Every year' };
  let label = names[parts.FREQ] || 'Custom';
  const units = { DAILY: 'days', WEEKLY: 'weeks', MONTHLY: 'months', YEARLY: 'years' };
  if (parts.INTERVAL && parts.INTERVAL !== '1' && units[parts.FREQ]) label = `Every ${parts.INTERVAL} ${units[parts.FREQ]}`;
  if (parts.FREQ === 'WEEKLY' && parts.BYDAY) {
    const days = parts.BYDAY.split(',');
    label = days.join(',') === 'MO,TU,WE,TH,FR' ? 'Every weekday'
      : `Every ${days.map((d) => WEEKDAYS_SHORT[RRULE_DAYS.indexOf(d)] || d).join(', ')}`;
  }
  if (parts.UNTIL) label += ` until ${dayLabel(`${parts.UNTIL.slice(0, 4)}-${parts.UNTIL.slice(4, 6)}-${parts.UNTIL.slice(6, 8)}`)}`;
  if (parts.COUNT) label += `, ${parts.COUNT} times`;
  return label;
}
