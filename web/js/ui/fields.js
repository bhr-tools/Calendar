// Date and time fields of the editor. Native <input type=date|time> follow the
// browser's locale (10/28/2026, 08:15 AM); these are text fields that always
// read "Wed 28 Oct" and "08:15", accept typing ("demain", "9h30", "930") and
// offer a picker: the browser's calendar for days, a list of quarter hours
// for times.

import { h, icon } from '../dom.js';
import { addDays, dayLabel, pad2, parseISO, todayISO } from '../dates.js';
import { parseClock, parseDay } from '../quickadd.js';

const STEP = 15;

function clock(minutes) {
  const m = ((minutes % 1440) + 1440) % 1440;
  return `${pad2(Math.floor(m / 60))}:${pad2(m % 60)}`;
}

/** "45 min", "1h", "1h45", "1 d 2h". */
export function durationLabel(minutes) {
  if (minutes < 60) return `${minutes} min`;
  const days = Math.floor(minutes / 1440);
  const hours = Math.floor((minutes % 1440) / 60);
  const rest = minutes % 60;
  const time = hours || rest ? `${hours}h${rest ? pad2(rest) : ''}` : '';
  return days ? `${days} d${time ? ` ${time}` : ''}` : time;
}

function dayText(iso) {
  if (!iso) return '';
  const label = dayLabel(iso);
  return parseISO(iso).getFullYear() === new Date().getFullYear() ? label : `${label} ${iso.slice(0, 4)}`;
}

/**
 * A day. Typing is parsed on Enter or blur; ↑ / ↓ move by one day; the icon
 * opens the browser's calendar. onChange(iso) runs when the day changes.
 * With `optional`, the field may be emptied (value '').
 */
export function dateField(value, { onChange, disabled = false, optional = false, placeholder = '' } = {}) {
  let day = value || '';
  const input = h('input', { class: 'field-input', value: dayText(day), placeholder, disabled, autocomplete: 'off', spellcheck: false });
  const native = h('input', { type: 'date', class: 'field-native', tabIndex: -1, value: day, attrs: { 'aria-hidden': 'true' } });
  const set = (iso, notify = true) => {
    const changed = iso !== day;
    day = iso;
    input.value = dayText(day);
    native.value = day;
    if (changed && notify) onChange?.(day);
  };
  const commit = () => {
    if (optional && !input.value.trim()) set('');
    else set(parseDay(input.value, todayISO()) || day);
  };
  input.addEventListener('keydown', (ev) => {
    if (ev.key === 'Enter') {
      ev.preventDefault();
      commit();
    } else if (ev.key === 'ArrowUp' || ev.key === 'ArrowDown') {
      ev.preventDefault();
      set(addDays(day || todayISO(), ev.key === 'ArrowUp' ? -1 : 1));
    }
  });
  input.addEventListener('blur', commit);
  input.addEventListener('focus', () => input.select());
  native.addEventListener('change', () => {
    if (native.value) set(native.value);
  });
  const button = h('button', {
    type: 'button', class: 'field-button', tabIndex: -1, title: 'Pick a day', disabled,
    onclick: () => { try { native.showPicker(); } catch { input.focus(); } },
  }, icon('calendar'));
  const el = h('span', { class: 'picker-field date-field' }, input, button, native);
  return {
    el,
    input,
    get value() { return day; },
    set value(iso) { set(iso, false); },
  };
}

/**
 * A time of day. Focusing it shows the quarter hours; ↑ / ↓ walk through
 * them, Enter or a click picks one, typing works too. With `after`, a
 * function returning a start in minutes, the list starts there and shows
 * durations ("10:00 · 1h45"): picks past midnight are returned as
 * minutes ≥ 1440. onChange(minutes) runs when the time changes.
 */
export function timeField(value, { onChange, after = null, disabled = false } = {}) {
  let minutes = value;
  const input = h('input', { class: 'field-input', value: clock(minutes), disabled, autocomplete: 'off', spellcheck: false, inputMode: 'numeric' });
  const list = h('div', { class: 'time-list', hidden: true });
  let items = [];
  let active = -1;

  const set = (m, notify = true) => {
    const changed = m !== minutes;
    minutes = m;
    input.value = clock(m);
    if (changed && notify) onChange?.(m);
  };

  const options = () => {
    const from = after?.();
    if (from == null) {
      return Array.from({ length: 1440 / STEP }, (_, i) => ({ minutes: i * STEP, label: clock(i * STEP) }));
    }
    const first = Math.floor(from / STEP) * STEP + STEP;
    return Array.from({ length: 1440 / STEP }, (_, i) => {
      const m = first + i * STEP;
      return { minutes: m, label: clock(m), meta: durationLabel(m - from) };
    });
  };

  const highlight = (i, scroll = true) => {
    items[active]?.el.classList.remove('active');
    active = Math.max(0, Math.min(items.length - 1, i));
    const item = items[active];
    item.el.classList.add('active');
    if (scroll) item.el.scrollIntoView({ block: 'nearest' });
  };

  const open = () => {
    if (!list.hidden || disabled) return;
    items = options().map((o, i) => ({
      ...o,
      el: h('div', {
        class: 'time-option',
        onmousedown: (ev) => { ev.preventDefault(); pick(i); },
      }, h('span', {}, o.label), o.meta ? h('span', { class: 'time-meta' }, o.meta) : null),
    }));
    list.replaceChildren(...items.map((i) => i.el));
    list.hidden = false;
    // Start on the current value, or the nearest quarter after it.
    let i = items.findIndex((o) => o.minutes >= minutes);
    if (i < 0) i = items.findIndex((o) => o.minutes % 1440 >= minutes % 1440);
    highlight(i < 0 ? 0 : i, false);
    list.scrollTop = Math.max(0, items[active].el.offsetTop - 2 * items[active].el.offsetHeight);
  };
  const close = () => {
    list.hidden = true;
    active = -1;
  };
  const pick = (i) => {
    close();
    set(items[i].minutes);
  };
  const commit = () => {
    const parsed = parseClock(input.value);
    if (parsed === null || parsed === minutes % 1440) input.value = clock(minutes);
    else set(parsed);
  };

  input.addEventListener('focus', () => {
    input.select();
    open();
  });
  input.addEventListener('click', open);
  input.addEventListener('blur', () => {
    close();
    commit();
  });
  input.addEventListener('input', close);
  input.addEventListener('keydown', (ev) => {
    if (ev.key === 'ArrowDown' || ev.key === 'ArrowUp') {
      ev.preventDefault();
      if (list.hidden) open();
      else highlight(active + (ev.key === 'ArrowDown' ? 1 : -1));
      input.value = items[active].label;
      input.select();
    } else if (ev.key === 'Enter') {
      ev.preventDefault();
      if (!list.hidden && items[active]?.label === input.value) pick(active);
      else {
        close();
        commit();
      }
    } else if (ev.key === 'Escape' && !list.hidden) {
      // Closes the list only, not the dialog.
      ev.preventDefault();
      ev.stopPropagation();
      close();
      input.value = clock(minutes);
      input.select();
    }
  });

  const el = h('span', { class: 'picker-field time-field' }, input, list);
  return {
    el,
    input,
    get value() { return minutes; },
    set value(m) { set(m, false); },
  };
}
