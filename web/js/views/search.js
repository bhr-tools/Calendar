// Advanced search: a full-screen page listing every occurrence that matches,
// past ones greyed out, with filters on the period, the calendars and the
// exact title. Opened from the quick search or from an event ("Same title").

import { api } from '../api.js';
import { h, icon } from '../dom.js';
import { MONTHS, WEEKDAYS_SHORT, hydrate, parseISO, spanLabel } from '../dates.js';
import { openModal } from '../ui/overlay.js';

const PERIODS = [['all', 'All'], ['upcoming', 'Upcoming'], ['past', 'Past']];

/** Opens the page. query: the text searched; exact: same title only. */
export function openAdvancedSearch(app, { query = '', exact = false } = {}) {
  const input = h('input', { class: 'picker-input', placeholder: 'Search events', autocomplete: 'off', value: query });
  const count = h('span', { class: 'search-count' });
  const periodSwitch = h('div', { class: 'segmented' });
  const exactBox = h('input', { type: 'checkbox', checked: exact });
  const calendarChips = h('div', { class: 'search-calendars' });
  const list = h('div', { class: 'search-results' });

  let results = []; // hydrated occurrences, in date order
  let period = 'all';
  const hiddenCalendars = new Set();
  let shown = []; // results after the filters
  let active = -1;
  let timer = 0;
  let loadId = 0;

  const isPast = (e) => e.end <= app.now();
  const titleOf = (e) => (e.title || '').trim().toLowerCase();

  const filtered = () => {
    const q = input.value.trim().toLowerCase();
    return results.filter((e) => (!exactBox.checked || titleOf(e) === q)
      && !hiddenCalendars.has(e.calendar_id)
      && (period === 'all' || (period === 'past') === isPast(e)));
  };

  const drawPeriods = () => periodSwitch.replaceChildren(...PERIODS.map(([value, label]) => h('button', {
    type: 'button',
    class: value === period && 'active',
    onclick: () => { period = value; drawPeriods(); draw({ scroll: true }); },
  }, label)));

  // Chips for the calendars present in the results, to leave some out.
  const drawCalendars = () => {
    const ids = [...new Set(results.map((e) => e.calendar_id))];
    calendarChips.replaceChildren(...(ids.length > 1 ? ids : []).map((id) => {
      const cal = app.calendarOf(id);
      return h('button', {
        type: 'button',
        class: ['search-chip', `cal-${cal?.color || 'pine'}`, hiddenCalendars.has(id) && 'off'],
        attrs: { 'aria-pressed': String(!hiddenCalendars.has(id)) },
        onclick: () => {
          if (hiddenCalendars.has(id)) hiddenCalendars.delete(id);
          else hiddenCalendars.add(id);
          drawCalendars();
          draw({ scroll: true });
        },
      }, h('span', { class: 'calendar-dot' }), cal?.name || 'Calendar');
    }));
  };

  const row = (e, i) => {
    const d = parseISO(e.startDay);
    const cal = app.calendarOf(e.calendar_id);
    return h('button', {
      type: 'button',
      class: ['search-row', isPast(e) && 'past', i === active && 'active', `cal-${cal?.color || 'pine'}`],
      dataset: { index: i },
      onclick: () => go(e),
    },
    h('span', { class: 'search-date' }, h('span', null, WEEKDAYS_SHORT[d.getDay()]), h('span', { class: 'search-day' }, d.getDate())),
    h('span', { class: 'calendar-dot' }),
    h('span', { class: 'search-main' },
      h('span', { class: 'search-title' }, e.title || '(No title)'),
      h('span', { class: 'search-meta' }, [spanLabel(e), e.location, cal?.name].filter(Boolean).join(' · '))));
  };

  const draw = ({ scroll = false } = {}) => {
    shown = filtered();
    if (active >= shown.length) active = shown.length - 1;
    const past = shown.filter(isPast).length;
    count.textContent = results.length || input.value.trim()
      ? `${shown.length} event${shown.length === 1 ? '' : 's'}${past && past < shown.length ? ` · ${past} past` : ''}`
      : '';
    if (!shown.length) {
      list.replaceChildren(h('div', { class: 'picker-empty' }, input.value.trim()
        ? (results.length ? 'No event matches the filters' : 'No event found')
        : 'Type to search titles, places and notes'));
      return;
    }
    // Grouped by month, with a marker where the upcoming events begin.
    const items = [];
    let month = '';
    let marked = false;
    shown.forEach((e, i) => {
      if (!marked && !isPast(e)) {
        marked = true;
        if (i > 0) items.push(h('div', { class: 'search-now' }, h('span', null, 'Today')));
      }
      const key = e.startDay.slice(0, 7);
      if (key !== month) {
        month = key;
        const d = parseISO(e.startDay);
        items.push(h('div', { class: 'search-month' }, `${MONTHS[d.getMonth()]} ${d.getFullYear()}`));
      }
      items.push(row(e, i));
    });
    list.replaceChildren(...items);
    if (scroll) {
      // Opens on what comes next; the past is just above.
      const now = list.querySelector('.search-now');
      // (The last past event stays in sight, below the sticky month heading.)
      list.scrollTop = now ? now.getBoundingClientRect().top - list.getBoundingClientRect().top + list.scrollTop - 110 : (period === 'past' ? list.scrollHeight : 0);
    }
    list.querySelector('.search-row.active')?.scrollIntoView({ block: 'nearest' });
  };

  const search = async () => {
    const q = input.value.trim();
    const id = ++loadId;
    const found = q ? ((await app.run(() => api.searchAll(q))) || []).map(hydrate) : [];
    if (id !== loadId) return;
    results = found;
    active = -1;
    drawCalendars();
    draw({ scroll: true });
  };

  const go = async (e) => {
    modal.close();
    app.state.anchor = e.startDay;
    if (app.state.view === 'month') app.state.view = 'week';
    await app.reload({ force: true });
    app.select(e.key);
  };

  const move = (step) => {
    if (!shown.length) return;
    if (active === -1) {
      // From nothing: the first upcoming event (or the last past one).
      const next = shown.findIndex((e) => !isPast(e));
      active = next === -1 ? shown.length - 1 : (step > 0 ? next : Math.max(0, next - 1));
    } else {
      active = Math.min(shown.length - 1, Math.max(0, active + step));
    }
    draw();
  };

  input.addEventListener('input', () => {
    clearTimeout(timer);
    timer = setTimeout(search, 150);
  });
  exactBox.addEventListener('change', () => draw({ scroll: true }));

  const modal = openModal({
    className: 'search-page',
    body: [
      h('div', { class: 'search-header' },
        h('button', { type: 'button', class: 'icon-button', title: 'Close (Esc)', onclick: () => modal.close() }, icon('chevronLeft')),
        h('div', { class: 'picker-search' }, icon('search'), input),
        count),
      h('div', { class: 'search-filters' },
        periodSwitch,
        h('label', { class: 'checkbox' }, exactBox, 'Exact title'),
        calendarChips),
      list,
    ],
  });
  modal.el.addEventListener('keydown', (ev) => {
    const inField = ev.target === input;
    const down = ev.key === 'ArrowDown' || (ev.ctrlKey && ev.key === 'j') || (!inField && ev.key === 'j');
    const up = ev.key === 'ArrowUp' || (ev.ctrlKey && ev.key === 'k') || (!inField && ev.key === 'k');
    if (down || up) {
      ev.preventDefault();
      move(down ? 1 : -1);
    } else if (ev.key === 'Enter' && (inField || ev.target === modal.el) && shown[active]) {
      ev.preventDefault();
      go(shown[active]);
    } else if (!inField && ev.key === '/') {
      ev.preventDefault();
      input.focus();
    }
  });

  drawPeriods();
  input.setSelectionRange(input.value.length, input.value.length);
  if (query) search();
  else draw();
  return modal;
}
