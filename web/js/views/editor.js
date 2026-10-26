// Event editor (a modal) and the small form shown when an event is drawn on
// the grid. Both build the payload of the API and hand it back to the app.

import { h, icon } from '../dom.js';
import { openModal, openPopover } from '../ui/overlay.js';
import { RRULE_DAYS, WEEKDAYS, addDays, dayLabel, hhmm, isoDate, parseISO, rruleLabel } from '../dates.js';

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
  const startDate = h('input', { type: 'date', class: 'input', value: startDay, disabled: readOnly });
  const startTime = h('input', { type: 'time', class: 'input', value: hhmm(e.start), step: 300, disabled: readOnly });
  // All-day ends are exclusive in data, inclusive on screen.
  const endShown = (e.all_day ?? e.allDay) ? addDays(isoDate(e.end), -1) : isoDate(e.end);
  const endDate = h('input', { type: 'date', class: 'input', value: endShown < startDay ? startDay : endShown, disabled: readOnly });
  const endTime = h('input', { type: 'time', class: 'input', value: hhmm(e.end), step: 300, disabled: readOnly });

  const { base, until } = splitUntil(e.rrule || '');
  const repeat = h('select', { class: 'input', disabled: readOnly },
    repeatOptions(startDay, base).map(([value, label]) => h('option', { value, selected: value === base }, label)));
  const untilInput = h('input', { type: 'date', class: 'input', value: until, title: 'Repeat until (optional)', disabled: readOnly });
  const untilRow = h('div', { class: 'editor-row' }, icon('chevronRight'), h('span', { class: 'field-label' }, 'Until'), untilInput);

  const calendar = readOnly ? null : calendarSelect(app, cal?.id);
  const location = h('input', { class: 'input', value: e.location || '', placeholder: 'Location', maxLength: 500, disabled: readOnly });
  const notes = h('textarea', { class: 'input', placeholder: 'Notes', disabled: readOnly });
  notes.value = e.notes || '';

  const times = h('div', { class: 'editor-times' });
  const layoutTimes = () => {
    const ad = allDay.checked;
    times.className = ['editor-times', ad && 'all-day'].filter(Boolean).join(' ');
    times.replaceChildren(...(ad
      ? [startDate, h('span', { class: 'dash' }, '–'), endDate]
      : [startDate, startTime, endDate, endTime]));
  };
  layoutTimes();
  allDay.addEventListener('change', layoutTimes);
  const syncUntil = () => { untilRow.hidden = !repeat.value; };
  syncUntil();
  repeat.addEventListener('change', syncUntil);

  // Moving the start keeps the duration.
  let lastStart = { date: startDate.value, time: startTime.value };
  const keepDuration = () => {
    const before = new Date(`${lastStart.date}T${lastStart.time || '00:00'}`);
    const after = new Date(`${startDate.value}T${startTime.value || '00:00'}`);
    const end = new Date(`${endDate.value}T${endTime.value || '00:00'}`);
    if (!Number.isNaN(before) && !Number.isNaN(after) && !Number.isNaN(end)) {
      const moved = new Date(end.getTime() + (after - before));
      endDate.value = isoDate(moved);
      if (!allDay.checked) endTime.value = hhmm(moved);
    }
    lastStart = { date: startDate.value, time: startTime.value };
  };
  startDate.addEventListener('change', keepDuration);
  startTime.addEventListener('change', keepDuration);

  const error = h('div', { class: 'form-error', hidden: true });

  const collect = () => {
    const ad = allDay.checked;
    let start;
    let end;
    if (ad) {
      start = parseISO(startDate.value);
      end = parseISO(addDays(endDate.value || startDate.value, 1));
    } else {
      start = new Date(`${startDate.value}T${startTime.value || '00:00'}`);
      end = new Date(`${endDate.value || startDate.value}T${endTime.value || '00:00'}`);
    }
    if (!start || !end || Number.isNaN(start.getTime()) || Number.isNaN(end.getTime())) throw new Error('Enter a valid start and end');
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
    title.focus();
    title.select();
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

