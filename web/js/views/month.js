// Month view: six weeks of days, events as chips. Dragging a chip to another
// day moves the event there at the same time (with Ctrl: copies it); a click
// on an empty part of a day creates an all-day event.

import { h } from '../dom.js';
import { follow } from '../drag.js';
import { WEEKDAYS_SHORT, addDays, daysBetween, hhmm, isoDate, parseISO } from '../dates.js';

export function renderMonth(app, days, month) {
  const today = app.today();
  const events = app.visibleEvents();

  const head = h('div', { class: 'month-head' },
    [1, 2, 3, 4, 5, 6, 0].map((d) => h('span', null, WEEKDAYS_SHORT[d])));

  const cells = days.map((day) => {
    const d = parseISO(day);
    const list = events
      .filter((e) => e.startDay <= day && e.endDay >= day)
      .sort((a, b) => (b.all_day - a.all_day) || (a.start - b.start));
    return h('div', {
      class: ['month-cell', d.getMonth() !== month && 'outside', day === today && 'today', day === app.state.anchor && 'anchor'],
      dataset: { day },
    },
    h('button', { type: 'button', class: 'month-day', title: 'Open this day', onclick: () => app.setView('day', day) }, d.getDate()),
    list.map((e) => {
      const cal = app.calendarOf(e.calendar_id);
      return h('div', {
        class: ['chip', e.all_day && 'all-day', `cal-${cal?.color || 'pine'}`, e.end < app.now() && 'past', app.state.selected === e.key && 'selected'],
        dataset: { key: e.key },
        title: e.title,
      },
      e.all_day ? null : h('span', { class: 'chip-time' }, e.startDay === day ? hhmm(e.start) : '…'),
      h('span', { class: 'chip-title' }, e.title || '(No title)'));
    }));
  });

  const grid = h('div', { class: 'month-grid' }, cells);
  const root = h('div', { class: 'month' }, head, grid);

  grid.addEventListener('pointerdown', (down) => {
    if (down.button !== 0 || down.target.closest('.month-day, .more-link')) return;
    const chip = down.target.closest('.chip');
    const cell = down.target.closest('.month-cell');
    if (!cell) return;
    if (!chip) {
      follow(down, {
        onClick: () => {
          const day = cell.dataset.day;
          app.openDraft({ allDay: true, start: parseISO(day), end: parseISO(addDays(day, 1)) }, cell);
        },
      });
      return;
    }
    const e = app.eventByKey(chip.dataset.key);
    if (!e) return;
    if (e.read_only) {
      follow(down, { onClick: () => app.openEvent(e) });
      return;
    }
    const fromDay = cell.dataset.day;
    let ghost = null;
    let target = null;
    let copy = false;
    const clear = () => {
      ghost?.remove();
      target?.classList.remove('drop-target');
      chip.classList.remove('dragging');
      app.renderStatus();
    };
    const update = (p) => {
      if (!ghost) {
        ghost = chip.cloneNode(true);
        ghost.classList.add('ghost');
        ghost.style.width = `${chip.offsetWidth}px`;
        document.body.append(ghost);
      }
      copy = p.copy;
      ghost.classList.toggle('copy', copy);
      chip.classList.toggle('dragging', !copy);
      ghost.style.left = `${p.x - chip.offsetWidth / 2}px`;
      ghost.style.top = `${p.y - 10}px`;
      const over = document.elementFromPoint(p.x, p.y)?.closest('.month-cell');
      if (over !== target) {
        target?.classList.remove('drop-target');
        target = over;
        target?.classList.add('drop-target');
      }
      app.renderStatus(copy ? 'COPY' : 'MOVE');
    };
    follow(down, {
      onStart: update,
      onMove: update,
      onEnd: () => {
        const toDay = target?.dataset.day;
        clear();
        if (!toDay) return;
        const delta = daysBetween(fromDay, toDay);
        if (!delta && !copy) return;
        if (e.all_day) {
          app.dropEvent(e, {
            allDay: true,
            start: parseISO(addDays(e.startDay, delta)),
            end: parseISO(addDays(isoDate(e.end), delta)),
          }, copy);
        } else {
          const start = new Date(e.start);
          start.setDate(start.getDate() + delta);
          app.dropEvent(e, { allDay: false, start, end: new Date(start.getTime() + (e.end - e.start)) }, copy);
        }
      },
      onClick: () => app.openEvent(e),
      onCancel: clear,
    });
  });

  /** Hides the chips that do not fit and adds "+N more" links. */
  root.fit = () => {
    for (const cell of cells) {
      cell.querySelector('.more-link')?.remove();
      const chips = [...cell.querySelectorAll('.chip')];
      chips.forEach((c) => { c.hidden = false; });
      const room = cell.clientHeight - 26;
      const fits = Math.max(0, Math.floor(room / 22));
      if (chips.length <= fits) continue;
      const shown = Math.max(0, fits - 1);
      chips.slice(shown).forEach((c) => { c.hidden = true; });
      const hiddenSelected = chips.slice(shown).some((c) => c.classList.contains('selected'));
      if (hiddenSelected) {
        // Keep the selection visible: show it in place of the last chip.
        chips.slice(shown).forEach((c) => { c.hidden = !c.classList.contains('selected'); });
        if (shown > 0) chips[shown - 1].hidden = true;
      }
      const count = chips.filter((c) => c.hidden).length;
      cell.append(h('button', {
        type: 'button',
        class: 'more-link',
        onclick: () => app.setView('day', cell.dataset.day),
      }, `+${count} more`));
    }
  };
  return root;
}
