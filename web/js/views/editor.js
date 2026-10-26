// Event editor (a modal) and the small form shown when an event is drawn on
// the grid. Both build the payload of the API and hand it back to the app.

import { h, icon } from '../dom.js';
import { openModal, openPopover } from '../ui/overlay.js';
import { MINUTE, RRULE_DAYS, WEEKDAYS, addDays, atMinutes, dayLabel, daysBetween, hhmm, isoDate, minutesOf, parseISO, rruleLabel } from '../dates.js';
import { dateField, durationLabel, timeField } from '../ui/fields.js';

/** Splits "FREQ=…;UNTIL=…" into the rule without UNTIL and the until day. */
function splitUntil(rule) {
  if (!rule) return { base: '', until: '' };
  const parts = rule.split(';');
  const until = parts.find((p) => p.startsWith('UNTIL='));
  const base = parts.filter((p) => !p.startsWith('UNTIL=')).join(';');
  const day = until ? until.slice(6, 14) : '';
  return { base, until: day ? `${day.slice(0, 4)}-${day.slice(4, 6)}-${day.slice(6, 8)}` : '' };
}

function repeatOptions(startDay, current) {
  const weekday = parseISO(startDay).getDay();
  const options = [
    ['', 'Does not repeat'],
    ['FREQ=DAILY', 'Every day'],
    [`FREQ=WEEKLY;BYDAY=${RRULE_DAYS[weekday]}`, `Every week on ${WEEKDAYS[weekday]}`],
    ['FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR', 'Every weekday (Mon – Fri)'],
    ['FREQ=MONTHLY', 'Every month'],
    ['FREQ=YEARLY', 'Every year'],
  ];
  if (current && !options.some(([v]) => v === current)) {
    // A rule written by another app: keep it as it is.
    options.push([current, rruleLabel(current)]);
  }
  return options;
}

function calendarSelect(app, selectedId) {
  const writable = app.writableCalendars();
  const select = h('select', { class: 'input' },
    writable.map((c) => h('option', { value: c.id, selected: c.id === selectedId }, c.name)));
  return select;
}

/**
 * Opens the editor. `e` is an existing occurrence, or a draft
 * { allDay, start, end, title?, calendar_id? } for a new event.
 */
export function openEditor(app, e, { isNew = false } = {}) {
  const readOnly = !isNew && e.read_only;
  const cal = app.calendarOf(e.calendar_id) || app.defaultCalendar();
  const startDay = isoDate(e.start);

  const title = h('input', { class: 'editor-title', value: e.title || '', placeholder: 'Add title', maxLength: 500, disabled: readOnly });
  const allDay = h('input', { type: 'checkbox', checked: !!(e.all_day ?? e.allDay), disabled: readOnly });

  // The span being edited. All-day ends are exclusive in data, inclusive here.
  const wasAllDay = allDay.checked;
  const span = {
    startDay,
    startMin: wasAllDay ? 9 * 60 : minutesOf(e.start),
    endDay: wasAllDay ? addDays(isoDate(e.end), -1) : isoDate(e.end),
    endMin: wasAllDay ? 10 * 60 : minutesOf(e.end),
  };
  if (span.endDay < span.startDay) span.endDay = span.startDay;
  const startAt = () => atMinutes(span.startDay, span.startMin);
  const endAt = () => atMinutes(span.endDay, span.endMin);
  const sameDay = () => span.endDay === span.startDay;

  const startDate = dateField(span.startDay, {
    disabled: readOnly,
    onChange: (day) => {
      // Moving the start keeps the length.
      span.endDay = addDays(span.endDay, daysBetween(span.startDay, day));
      span.startDay = day;
      refresh();
    },
  });
  const startTime = timeField(span.startMin, {
    disabled: readOnly,
    onChange: (m) => {
      const end = new Date(endAt().getTime() + (m - span.startMin) * MINUTE);
      span.startMin = m;
      span.endDay = isoDate(end);
      span.endMin = minutesOf(end);
      refresh();
    },
  });
  const endDate = dateField(span.endDay, {
    disabled: readOnly,
    onChange: (day) => {
      span.endDay = day;
      refresh();
    },
  });
  const endTime = timeField(span.endMin, {
    disabled: readOnly,
    // On a one-day event the list counts from the start, with durations.
    after: () => (allDay.checked || !sameDay() ? null : span.startMin),
    onChange: (m) => {
      if (m >= 1440) {
        span.endDay = addDays(span.startDay, 1);
        span.endMin = m - 1440;
      } else {
        span.endMin = m;
        // Ending before it starts on the same day means the next day.
        if (sameDay() && m <= span.startMin) span.endDay = addDays(span.startDay, 1);
      }
      refresh();
    },
  });
  const duration = h('span', { class: 'editor-duration' });

  const { base, until } = splitUntil(e.rrule || '');
  const repeat = h('select', { class: 'input', disabled: readOnly },
    repeatOptions(startDay, base).map(([value, label]) => h('option', { value, selected: value === base }, label)));
  const untilInput = dateField(until, { optional: true, placeholder: 'Forever', disabled: readOnly });
  const untilRow = h('div', { class: 'editor-row' }, h('span', { class: 'icon' }), h('span', { class: 'field-label' }, 'Until'), untilInput.el);

  const calendar = readOnly ? null : calendarSelect(app, cal?.id);
  const location = h('input', { class: 'input', value: e.location || '', placeholder: 'Location', maxLength: 500, disabled: readOnly });
  const notes = h('textarea', { class: 'input', placeholder: 'Notes', disabled: readOnly });
  notes.value = e.notes || '';

  // "Wed 28 Oct  08:15 – 10:00  1h45"; the end day only shows when it differs.
  const times = h('div', { class: 'editor-times' },
    startDate.el, startTime.el, h('span', { class: 'dash' }, '–'), endDate.el, endTime.el, duration);
  endDate.el.classList.add('end');
  const refresh = () => {
    startDate.value = span.startDay;
    startTime.value = span.startMin;
    endDate.value = span.endDay;
    endTime.value = span.endMin;
    const minutes = Math.round((endAt() - startAt()) / MINUTE);
    duration.textContent = minutes > 0 ? durationLabel(minutes) : '';
    // CSS shows the fields each shape needs.
    times.className = `editor-times ${allDay.checked ? 'all-day' : sameDay() ? 'one-day' : 'days'}`;
  };
  refresh();
  allDay.addEventListener('change', refresh);
  const syncUntil = () => { untilRow.hidden = !repeat.value; };
  syncUntil();
  repeat.addEventListener('change', syncUntil);

  const error = h('div', { class: 'form-error', hidden: true });

  const collect = () => {
    const ad = allDay.checked;
    const start = ad ? parseISO(span.startDay) : startAt();
    const end = ad ? parseISO(addDays(span.endDay, 1)) : endAt();
    if (end < start || (ad && end <= start)) throw new Error('The event ends before it starts');
    let rule = repeat.value;
    if (rule && untilInput.value) rule += `;UNTIL=${untilInput.value.replaceAll('-', '')}`;
    return {
      calendar_id: calendar?.value || e.calendar_id,
      title: title.value.trim(),
      location: location.value.trim(),
      notes: notes.value,
      all_day: ad,
      start,
      end,
      rrule: rule,
      rruleChanged: rule !== (e.rrule || ''),
    };
  };

  let modal;
  const save = async () => {
    let fields;
    try {
      fields = collect();
    } catch (err) {
      error.textContent = err.message;
      error.hidden = false;
      return;
    }
    const ok = isNew ? await app.createEvent(fields) : await app.saveEvent(e, fields);
    if (ok) modal.close();
  };

  const row = (iconName, ...content) => h('div', { class: 'editor-row' }, icon(iconName), ...content);
  const body = [
    title,
    row('clock', times),
    readOnly ? null : h('div', { class: 'editor-row' }, h('span', { class: 'icon' }), h('label', { class: 'checkbox' }, allDay, 'All day')),
    readOnly ? (e.recurring ? row('repeat', h('span', { class: 'editor-readonly' }, rruleLabel(e.rrule) || 'Repeats')) : null) : row('repeat', repeat),
    readOnly ? null : untilRow,
    row('calendar', readOnly
      ? h('span', { class: ['editor-calendar', `cal-${cal?.color}`] }, h('span', { class: 'calendar-dot' }), cal?.name, h('span', { class: 'editor-readonly' }, '· read-only'))
      : calendar),
    readOnly && !e.location ? null : row('mapPin', location),
    readOnly
      ? (e.notes ? h('div', { class: 'editor-row top' }, icon('notes'), h('div', { class: 'editor-notes' }, e.notes)) : null)
      : h('div', { class: 'editor-row top' }, icon('notes'), notes),
    error,
  ];

  const footer = readOnly
    ? [h('span', { class: 'spacer' }), h('button', { type: 'button', class: 'button', onclick: () => modal.close() }, 'Close')]
    : [
      isNew ? null : h('button', { type: 'button', class: 'button ghost danger-text', onclick: async () => { if (await app.deleteEvent(e)) modal.close(); } }, icon('trash'), 'Delete'),
      isNew ? null : h('button', { type: 'button', class: 'button ghost', title: 'Duplicate (copy and paste)', onclick: async () => { modal.close(); await app.duplicate(e); } }, icon('copy'), 'Duplicate'),
      h('span', { class: 'spacer' }),
      h('button', { type: 'button', class: 'button', onclick: () => modal.close() }, 'Cancel'),
      h('button', { type: 'button', class: 'button primary', onclick: save, title: 'Save (Ctrl+Enter)' }, isNew ? 'Create' : 'Save'),
    ];

  modal = openModal({ className: 'editor', body, footer });
  modal.el.addEventListener('keydown', (ev) => {
    if (readOnly) return;
    if (ev.key === 'Enter' && (ev.ctrlKey || ev.metaKey || ev.target === title)) {
      ev.preventDefault();
      save();
    }
  });
  if (!readOnly) {
    // Cursor at the end of the title, ready to type.
    title.focus();
    title.setSelectionRange(title.value.length, title.value.length);
  }
  return modal;
}

/**
 * The small form shown next to an event drawn on the grid: a title, the
 * calendar, Enter to create. "More options" opens the full editor.
 */
export function openDraftForm(app, draft, anchor, onClose) {
  const title = h('input', { class: 'input', placeholder: 'Add title', maxLength: 500, autocomplete: 'off' });
  const calendar = calendarSelect(app, app.defaultCalendar()?.id);
  const when = draft.allDay
    ? `${dayLabel(isoDate(draft.start))} · all day`
    : `${dayLabel(isoDate(draft.start))} · ${hhmm(draft.start)} – ${hhmm(draft.end)}`;
  let close;
  const fields = () => ({
    calendar_id: calendar.value,
    title: title.value.trim(),
    location: '',
    notes: '',
    all_day: draft.allDay,
    start: draft.start,
    end: draft.end,
    rrule: '',
  });
  const create = async () => {
    const f = fields();
    close();
    await app.createEvent(f);
  };
  const more = () => {
    const f = fields();
    close();
    openEditor(app, { ...f, allDay: f.all_day }, { isNew: true });
  };
  const form = h('div', { class: 'draft-form' },
    title,
    h('div', { class: 'draft-when' }, when),
    calendar,
    h('div', { class: 'draft-actions' },
      h('button', { type: 'button', class: 'button ghost', onclick: more }, 'More options'),
      h('button', { type: 'button', class: 'button primary', onclick: create }, 'Create')));
  form.addEventListener('keydown', (ev) => {
    if (ev.key === 'Enter') {
      ev.preventDefault();
      create();
    }
  });
  close = openPopover(anchor, form, { onClose, title: 'New event' });
  title.focus();
}

