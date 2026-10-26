// Time grid for the day and week views: one column per day, all-day events
// in a strip above. Dragging on empty space creates an event, dragging an
// event moves it (with Ctrl: copies it), dragging its bottom edge resizes it.

import { h } from '../dom.js';
import { follow } from '../drag.js';
import { MINUTE, WEEKDAYS_SHORT, addDays, atMinutes, daysBetween, hhmm, hourLabel, isoDate, minutesOf, parseISO } from '../dates.js';

export const SNAP = 15; // minutes

/** Splits timed events into one segment per day they cover. */
function segments(events, days) {
  const list = [];
  for (const e of events) {
    if (e.all_day) continue;
    days.forEach((day, index) => {
      const dayStart = parseISO(day);
      const dayEnd = parseISO(addDays(day, 1));
      const instant = e.end.getTime() === e.start.getTime();
      if (instant ? (e.start >= dayStart && e.start < dayEnd) : (e.start < dayEnd && e.end > dayStart)) {
        const start = e.start > dayStart ? e.start : dayStart;
        const end = e.end < dayEnd ? e.end : dayEnd;
        list.push({ e, index, startMin: minutesOf(start), endMin: end >= dayEnd ? 24 * 60 : minutesOf(end) });
      }
    });
  }
  return list;
}

/**
 * Places overlapping segments side by side: each group of overlapping
 * segments is split into columns, every segment taking the first free one.
 */
function layout(segs) {
  const byDay = new Map();
  for (const s of segs) {
    if (!byDay.has(s.index)) byDay.set(s.index, []);
    byDay.get(s.index).push(s);
  }
  for (const list of byDay.values()) {
    list.sort((a, b) => a.startMin - b.startMin || (b.endMin - b.startMin) - (a.endMin - a.startMin));
    let group = [];
    let groupEnd = -1;
    const flush = () => {
      const columns = [];
      for (const s of group) {
        let col = columns.findIndex((end) => end <= s.startMin);
        if (col === -1) {
          col = columns.length;
          columns.push(0);
        }
        columns[col] = Math.max(s.endMin, s.startMin + SNAP);
        s.col = col;
      }
      for (const s of group) s.cols = columns.length;
    };
    for (const s of list) {
      if (s.startMin >= groupEnd && group.length) {
        flush();
        group = [];
      }
      group.push(s);
      groupEnd = Math.max(groupEnd, Math.max(s.endMin, s.startMin + SNAP));
    }
    if (group.length) flush();
  }
  return segs;
}

/** All-day events as bars over the visible days, packed into lanes. */
function bars(events, days) {
  const first = days[0];
  const last = days[days.length - 1];
  const list = events
    .filter((e) => e.all_day && e.startDay <= last && e.endDay >= first)
    .map((e) => ({
      e,
      from: Math.max(0, daysBetween(first, e.startDay)),
      to: Math.min(days.length - 1, daysBetween(first, e.endDay)),
    }))
    .sort((a, b) => a.from - b.from || (b.to - b.from) - (a.to - a.from));
  const lanes = [];
  for (const b of list) {
    let lane = lanes.findIndex((end) => end < b.from);
    if (lane === -1) {
      lane = lanes.length;
      lanes.push(-1);
    }
    lanes[lane] = b.to;
    b.lane = lane;
  }
  return { list, lanes: lanes.length };
}

function eventClasses(app, e, extra = []) {
  const cal = app.calendarOf(e.calendar_id);
  return [
    `cal-${cal?.color || 'pine'}`,
    e.end < app.now() && 'past',
    e.cancelled && 'cancelled',
    e.read_only && 'readonly',
    app.state.selected === e.key && 'selected',
    ...extra,
  ];
}

export function renderGrid(app, days) {
  const today = app.today();
  const hourPx = () => parseFloat(getComputedStyle(root).getPropertyValue('--hour')) || 48;
  const events = app.visibleEvents();

  // --- Head ---
  const head = h('div', { class: 'grid-head' },
    h('div'),
    days.map((day) => {
      const d = parseISO(day);
      return h('div', {
        class: ['day-head', day === today && 'today', day === app.state.anchor && 'anchor'],
        title: 'Open this day',
        onclick: () => app.setView('day', day),
      }, h('span', null, WEEKDAYS_SHORT[d.getDay()]), h('span', { class: 'day-number' }, d.getDate()));
    }));

  // --- All-day strip ---
  const { list: barList, lanes } = bars(events, days);
  const allday = h('div', { class: 'grid-allday', style: { gridTemplateRows: `repeat(${Math.max(1, lanes)}, 24px)` } },
    h('div', { class: 'allday-label' }, 'all day'),
    days.map((day, i) => h('div', {
      class: 'allday-cell',
      style: { gridColumn: `${i + 2}` },
      dataset: { day },
    })),
    barList.map((b) => h('div', {
      class: ['bar', ...eventClasses(app, b.e)],
      style: { gridColumn: `${b.from + 2} / ${b.to + 3}`, gridRow: `${b.lane + 1}` },
      dataset: { key: b.e.key },
      title: b.e.title,
    }, h('span', null, b.e.title || '(No title)'))));

  // --- Hours ---
  const labels = h('div', { class: 'hour-labels' });
  for (let hr = 1; hr < 24; hr++) {
    labels.append(h('span', { class: 'hour-label', style: { top: `calc(${hr} * var(--hour))` } }, hourLabel(hr)));
  }
  const columns = days.map((day) => {
    const d = parseISO(day);
    return h('div', {
      class: ['day-col', day === today && 'today', (d.getDay() === 0 || d.getDay() === 6) && 'weekend'],
      dataset: { day },
    });
  });
  for (const s of layout(segments(events, days))) {
    const height = `max(calc(${(s.endMin - s.startMin) / 60} * var(--hour) - 2px), 14px)`;
    const short = s.endMin - s.startMin < 45;
    const el = h('div', {
      class: ['event', short && 'short', ...eventClasses(app, s.e)],
      dataset: { key: s.e.key },
      style: {
        top: `calc(${s.startMin / 60} * var(--hour) + 1px)`,
        height,
        left: `calc(${(s.col / s.cols) * 100}% + 1px)`,
        width: `calc(${100 / s.cols}% - 3px)`,
      },
      title: `${s.e.title || '(No title)'}\n${hhmm(s.e.start)} – ${hhmm(s.e.end)}${s.e.location ? `\n${s.e.location}` : ''}`,
    },
    h('span', { class: 'event-title' }, s.e.title || '(No title)'),
    h('span', { class: 'event-time' }, `${hhmm(s.e.start)} – ${hhmm(s.e.end)}`),
    s.e.location && !short ? h('span', { class: 'event-location' }, s.e.location) : null,
    s.e.read_only ? null : h('div', { class: 'resize-handle' }));
    columns[s.index].append(el);
  }
  const nowLine = h('div', { class: 'now-line' });
  const todayIndex = days.indexOf(today);
  if (todayIndex !== -1) columns[todayIndex].append(nowLine);

  const hours = h('div', { class: 'grid-hours' }, labels, columns);
  const body = h('div', { class: 'grid-body' }, hours);
  const root = h('div', { class: 'grid', style: { '--days': days.length } }, head, allday, body);

  const placeNow = () => {
    nowLine.style.top = `calc(${minutesOf(new Date()) / 60} * var(--hour))`;
  };
  placeNow();

  // --- Pointer to time ---
  const slotAt = (x, y, { snap = SNAP } = {}) => {
    let index = columns.findIndex((col) => {
      const r = col.getBoundingClientRect();
      return x >= r.left && x < r.right;
    });
    if (index === -1) {
      const first = columns[0].getBoundingClientRect();
      index = x < first.left ? 0 : columns.length - 1;
    }
    const top = hours.getBoundingClientRect().top;
    const raw = ((y - top) / hourPx()) * 60;
    const minutes = Math.max(0, Math.min(24 * 60, Math.round(raw / snap) * snap));
    return { index, minutes };
  };

  // --- Gestures ---
  hours.addEventListener('pointerdown', (down) => {
    if (down.button !== 0) return;
    const eventEl = down.target.closest('.event');
    if (eventEl) return dragEvent(down, eventEl, down.target.classList.contains('resize-handle'));
    const col = down.target.closest('.day-col');
    if (col) dragCreate(down);
  });

  allday.addEventListener('pointerdown', (down) => {
    if (down.button !== 0) return;
    const barEl = down.target.closest('.bar');
    if (barEl) return dragBar(down, barEl);
    const cell = down.target.closest('.allday-cell');
    if (!cell) return;
    follow(down, {
      onClick: () => {
        const day = cell.dataset.day;
        app.openDraft({ allDay: true, start: parseISO(day), end: parseISO(addDays(day, 1)) }, cell);
      },
    });
  });

  function dragCreate(down) {
    const startSlot = slotAt(down.clientX, down.clientY);
    const color = `cal-${app.defaultCalendar()?.color || 'pine'}`;
    let ghost = null;
    let range = null;
    const show = (index, a, b) => {
      if (!ghost) {
        ghost = h('div', { class: ['event', 'ghost', 'draft', color] }, h('span', { class: 'event-time' }));
        columns[index].append(ghost);
      }
      Object.assign(ghost.style, {
        top: `calc(${a / 60} * var(--hour) + 1px)`,
        height: `calc(${(b - a) / 60} * var(--hour) - 2px)`,
        left: '1px',
        width: 'calc(100% - 3px)',
      });
      ghost.firstChild.textContent = `${hhmm(atMinutes(days[index], a))} – ${hhmm(atMinutes(days[index], b))}`;
      range = { index, a, b };
    };
    const update = (p) => {
      const slot = slotAt(p.x, p.y);
      const a = Math.min(startSlot.minutes, slot.minutes);
      show(startSlot.index, a, Math.max(a + SNAP, startSlot.minutes, slot.minutes));
    };
    const open = () => {
      const g = ghost;
      const day = days[range.index];
      app.openDraft({ allDay: false, start: atMinutes(day, range.a), end: atMinutes(day, range.b) }, g, () => g.remove());
    };
    follow(down, {
      onStart: update,
      onMove: update,
      onEnd: open,
      onClick: () => {
        // A click creates an hour starting on the quarter it hit (rounded down).
        const slot = slotAt(down.clientX, down.clientY - (hourPx() * SNAP) / 120);
        const a = Math.min(slot.minutes, 23 * 60);
        show(slot.index, a, a + 60);
        open();
      },
      onCancel: () => ghost?.remove(),
    });
  }

  function dragEvent(down, el, resize) {
    const e = app.eventByKey(el.dataset.key);
    if (!e) return;
    if (e.read_only) {
      follow(down, { onClick: () => app.openEvent(e) });
      return;
    }
    const startSlot = slotAt(down.clientX, down.clientY, { snap: 1 });
    let result = null;
    let ghosts = [];
    const clear = () => {
      ghosts.forEach((g) => g.remove());
      ghosts = [];
      root.querySelectorAll(`.event[data-key="${CSS.escape(e.key)}"]`).forEach((n) => n.classList.remove('dragging'));
    };
    const update = (p) => {
      const slot = slotAt(p.x, p.y, { snap: 1 });
      let start;
      let end;
      if (resize) {
        start = e.start;
        const endSlot = slotAt(p.x, p.y);
        end = atMinutes(days[endSlot.index], endSlot.minutes);
        if (end - start < SNAP * MINUTE) end = new Date(start.getTime() + SNAP * MINUTE);
      } else {
        const deltaDays = slot.index - startSlot.index;
        const deltaMin = Math.round((slot.minutes - startSlot.minutes) / SNAP) * SNAP;
        start = new Date(e.start);
        start.setDate(start.getDate() + deltaDays);
        start.setMinutes(start.getMinutes() + deltaMin);
        end = new Date(start.getTime() + (e.end - e.start));
      }
      result = { start, end, copy: p.copy && !resize };
      clear();
      if (!result.copy) {
        root.querySelectorAll(`.event[data-key="${CSS.escape(e.key)}"]`).forEach((n) => n.classList.add('dragging'));
      }
      const moved = { ...e, start, end };
      for (const s of segments([moved], days)) {
        const g = h('div', {
          class: ['event', 'ghost', result.copy && 'copy', ...eventClasses(app, e).filter((c) => c !== 'selected' && c !== 'past')],
          style: {
            top: `calc(${s.startMin / 60} * var(--hour) + 1px)`,
            height: `max(calc(${(s.endMin - s.startMin) / 60} * var(--hour) - 2px), 14px)`,
            left: '1px',
            width: 'calc(100% - 3px)',
          },
        }, h('span', { class: 'event-title' }, e.title || '(No title)'), h('span', { class: 'event-time' }, `${hhmm(start)} – ${hhmm(end)}`));
        columns[s.index].append(g);
        ghosts.push(g);
      }
      app.renderStatus(result.copy ? 'COPY' : 'MOVE');
    };
    follow(down, {
      onStart: update,
      onMove: update,
      onEnd: () => {
        clear();
        app.renderStatus();
        if (result && (result.start.getTime() !== e.start.getTime() || result.end.getTime() !== e.end.getTime() || result.copy)) {
          app.dropEvent(e, { allDay: false, start: result.start, end: result.end }, result.copy);
        }
      },
      onClick: () => app.openEvent(e),
      onCancel: () => {
        clear();
        app.renderStatus();
      },
    });
  }

  function dragBar(down, el) {
    const e = app.eventByKey(el.dataset.key);
    if (!e) return;
    if (e.read_only) {
      follow(down, { onClick: () => app.openEvent(e) });
      return;
    }
    const startIndex = slotAt(down.clientX, down.clientY).index;
    let delta = 0;
    let copy = false;
    const update = (p) => {
      delta = slotAt(p.x, p.y).index - startIndex;
      copy = p.copy;
      el.style.transform = `translateX(${delta * columns[0].getBoundingClientRect().width}px)`;
      el.classList.toggle('copy', copy);
      app.renderStatus(copy ? 'COPY' : 'MOVE');
    };
    follow(down, {
      onStart: update,
      onMove: update,
      onEnd: () => {
        el.style.transform = '';
        app.renderStatus();
        if (delta || copy) {
          app.dropEvent(e, {
            allDay: true,
            start: parseISO(addDays(e.startDay, delta)),
            end: parseISO(addDays(isoDate(e.end), delta)),
          }, copy);
        }
      },
      onClick: () => app.openEvent(e),
      onCancel: () => {
        el.style.transform = '';
        app.renderStatus();
      },
    });
  }

  root.placeNow = placeNow;
  /** Scrolls so that `minutes` is at the top. */
  root.scrollToTime = (minutes) => {
    body.scrollTop = Math.max(0, (minutes / 60) * hourPx() - 8);
  };
  /** Keeps the selected event visible. */
  root.reveal = (e) => {
    if (!e || e.all_day) return;
    const top = (minutesOf(e.start) / 60) * hourPx();
    const bottom = top + ((e.end - e.start) / 3600000) * hourPx();
    if (top < body.scrollTop + 20 || bottom > body.scrollTop + body.clientHeight - 20) {
      body.scrollTop = Math.max(0, top - body.clientHeight / 3);
    }
  };
  root.body = body;
  return root;
}

