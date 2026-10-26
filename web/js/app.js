// Calendar web app: state, actions and orchestration of the views.

import { api, ApiError, onUnauthorized } from './api.js';
import { h, icon, isMobile } from './dom.js';
import {
  MINUTE, MONTHS, MONTHS_SHORT, addDays, addMonths, dayLabel, hhmm, hydrate, isoDate, longDayLabel,
  parseISO, todayISO, weekStart, wireTimes,
} from './dates.js';
import { closeAllModals, closePopover, confirmDialog, openMenu, openModal, toast, topModal } from './ui/overlay.js';
import { renderGrid } from './views/grid.js';
import { renderMonth } from './views/month.js';
import { openDraftForm, openEditor } from './views/editor.js';
import { renderSidebar } from './views/sidebar.js';
import { initKeys } from './keys.js';
import { parseQuickAdd } from './quickadd.js';

// Same order as the server: consecutive colors differ in hue.
const COLORS = ['pine', 'rose', 'iris', 'gold', 'foam', 'love'];
const POLL = 60_000; // reload from the server, for changes made on other devices
const $ = (id) => document.getElementById(id);

// Per-device preferences; the app works without them (private windows).
const prefs = {
  get(key, fallback) {
    try {
      return localStorage.getItem(`calendar.${key}`) ?? fallback;
    } catch {
      return fallback;
    }
  },
  set(key, value) {
    try {
      localStorage.setItem(`calendar.${key}`, value);
    } catch { /* not persisted */ }
  },
};

class App {
  constructor() {
    this.state = {
      auth: { no_auth: false, has_password: true, authenticated: false },
      view: prefs.get('view', isMobile() ? 'day' : 'week'), // day | week | month
      anchor: todayISO(), // the focused day
      today: todayISO(),
      calendars: [],
      events: [], // hydrated occurrences of the loaded range
      loaded: null, // { from, to } days, to exclusive
      selected: null, // key of the selected occurrence
      clipboard: null, // copied occurrence (y)
    };
    this.viewEl = null;
    this.loadId = 0;
    this.queue = Promise.resolve(); // serializes keyboard edits
  }

  async start() {
    this.bindChrome();
    initKeys(this);
    onUnauthorized(() => this.showLogin());
    setInterval(() => this.tick(), POLL);
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'visible') this.tick();
    });
    window.addEventListener('resize', () => this.viewEl?.fit?.());
    await this.boot();
  }

  /** Follows the day and the server while the app stays open. */
  tick() {
    if (!this.state.auth.authenticated || document.visibilityState !== 'visible') return;
    if (todayISO() !== this.state.today) {
      this.state.today = todayISO();
      this.render();
    }
    this.viewEl?.placeNow?.();
    if (!topModal()) this.reload();
  }

  async boot() {
    let status;
    try {
      status = await api.auth.status();
    } catch (err) {
      this.showFatal(err);
      return;
    }
    this.state.auth = status;
    if (!status.no_auth && !status.has_password) return this.showSetup();
    if (!status.authenticated) return this.showLogin();
    await this.loadAll();
  }

  // -------------------------------------------------------------------------
  // Data
  // -------------------------------------------------------------------------

  /** The days shown by the current view. */
  days() {
    const { view, anchor } = this.state;
    if (view === 'day') return [anchor];
    const first = view === 'week' ? weekStart(anchor) : weekStart(`${anchor.slice(0, 8)}01`);
    const count = view === 'week' ? 7 : 42;
    return Array.from({ length: count }, (_, i) => addDays(first, i));
  }

  async loadAll() {
    await this.run(async () => {
      this.state.calendars = await api.calendars.list();
    });
    await this.reload({ force: true });
  }

  /** Loads the events around the visible days (a few weeks of margin). */
  async reload({ force = false } = {}) {
    const days = this.days();
    const from = addDays(days[0], -14);
    const to = addDays(days[days.length - 1], 15);
    const loaded = this.state.loaded;
    const covered = loaded && loaded.from <= days[0] && loaded.to > days[days.length - 1];
    if (covered && !force) {
      // Still refresh in the background, without blocking navigation.
      this.fetchEvents(loaded.from, loaded.to);
      return;
    }
    if (!covered) this.render();
    await this.fetchEvents(from, to);
  }

  async fetchEvents(from, to) {
    const id = ++this.loadId;
    try {
      const [calendars, events] = await Promise.all([api.calendars.list(), api.events.list(from, to)]);
      if (id !== this.loadId) return;
      this.state.calendars = calendars;
      this.state.events = events.map(hydrate);
      this.state.loaded = { from, to };
      if (this.state.selected && !this.eventByKey(this.state.selected)) this.state.selected = null;
      this.render();
    } catch (err) {
      if (!(err instanceof ApiError && err.status === 401)) toast(err.message, { type: 'error' });
    }
  }

  /** Runs fn, showing its error as a toast. Returns fn's result or undefined. */
  async run(fn) {
    try {
      return await fn();
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) return undefined; // the login dialog is shown
      toast(err.message, { type: 'error' });
      return undefined;
    }
  }

  calendarOf(id) {
    return this.state.calendars.find((c) => c.id === id);
  }

  writableCalendars() {
    return this.state.calendars.filter((c) => !c.source_url);
  }

  /** Where new events go: the last calendar used, else the first one. */
  defaultCalendar() {
    const writable = this.writableCalendars();
    const last = prefs.get('lastCalendar', '');
    return writable.find((c) => c.id === last && !c.hidden) || writable.find((c) => !c.hidden) || writable[0];
  }

  visibleEvents() {
    const hidden = new Set(this.state.calendars.filter((c) => c.hidden).map((c) => c.id));
    return this.state.events.filter((e) => !hidden.has(e.calendar_id));
  }

  eventByKey(key) {
    return this.state.events.find((e) => e.key === key);
  }

  now() {
    return new Date();
  }

  today() {
    return this.state.today;
  }

  /** Visible events of the visible days, in reading order. */
  order() {
    const days = this.days();
    const first = days[0];
    const last = days[days.length - 1];
    return this.visibleEvents()
      .filter((e) => e.endDay >= first && e.startDay <= last)
      .sort((a, b) => a.startDay.localeCompare(b.startDay) || (b.all_day - a.all_day) || (a.start - b.start) || a.title.localeCompare(b.title));
  }

  // -------------------------------------------------------------------------
  // Rendering
  // -------------------------------------------------------------------------

  render() {
    const { view, anchor } = this.state;
    const days = this.days();
    const d = parseISO(anchor);
    let title;
    if (view === 'month') {
      title = [MONTHS[d.getMonth()], h('span', { class: 'muted' }, ` ${d.getFullYear()}`)];
    } else if (view === 'week') {
      const a = parseISO(days[0]);
      const b = parseISO(days[6]);
      title = a.getMonth() === b.getMonth()
        ? [MONTHS[a.getMonth()], h('span', { class: 'muted' }, ` ${a.getFullYear()}`)]
        : [`${MONTHS_SHORT[a.getMonth()]} – ${MONTHS_SHORT[b.getMonth()]}`, h('span', { class: 'muted' }, ` ${b.getFullYear()}`)];
    } else {
      title = isMobile() ? dayLabel(anchor) : longDayLabel(anchor);
    }
    $('view-title').replaceChildren(...[title].flat());
    document.title = view === 'day' ? `${dayLabel(anchor)} · Calendar` : `${MONTHS[d.getMonth()]} ${d.getFullYear()} · Calendar`;
    for (const b of $('view-switch').querySelectorAll('button')) b.classList.toggle('active', b.dataset.view === view);

    const previous = this.viewEl;
    const scroll = previous?.body?.scrollTop;
    this.viewEl = view === 'month' ? renderMonth(this, days, parseISO(`${anchor.slice(0, 8)}01`).getMonth()) : renderGrid(this, days);
    $('view').replaceChildren(this.viewEl);
    if (view === 'month') {
      this.viewEl.fit();
    } else if (scroll !== undefined && previous?.days === days.length) {
      this.viewEl.body.scrollTop = scroll;
    } else {
      const start = this.state.anchor === this.state.today ? Math.max(0, new Date().getHours() * 60 - 60) : 7 * 60;
      this.viewEl.scrollToTime(start);
    }
    this.viewEl.days = days.length;
    renderSidebar(this, $('calendar-list'));
    this.renderStatus();
  }

  renderStatus(mode) {
    const el = $('status-mode');
    const label = mode || (document.activeElement?.matches?.('input, textarea, select') ? 'INSERT' : 'NORMAL');
    el.textContent = `-- ${label} --`;
    el.className = `status-mode ${label === 'INSERT' ? 'insert' : (label === 'COPY' ? 'copy' : '')}`;
    const order = this.order();
    const index = order.findIndex((e) => e.key === this.state.selected);
    $('status-position').textContent = order.length ? `${index + 1 || '-'} / ${order.length}` : '';
  }

  // -------------------------------------------------------------------------
  // Navigation and selection
  // -------------------------------------------------------------------------

  setView(view, day) {
    this.state.view = view;
    if (day) this.state.anchor = day;
    prefs.set('view', view);
    this.reload();
    this.render();
  }

  setAnchor(day) {
    this.state.anchor = day;
    this.reload();
    this.render();
  }

  /** Moves by one period (day, week or month). */
  shift(direction) {
    const { view, anchor } = this.state;
    this.state.selected = null;
    if (view === 'month') this.setAnchor(addMonths(anchor, direction));
    else this.setAnchor(addDays(anchor, direction * (view === 'week' ? 7 : 1)));
  }

  goToday() {
    this.state.selected = null;
    this.setAnchor(todayISO());
  }

  select(key) {
    this.state.selected = key;
    const e = key && this.eventByKey(key);
    if (e && this.days().includes(e.startDay)) this.state.anchor = e.startDay;
    this.render();
    if (e) this.viewEl?.reveal?.(e);
  }

  /** j / k: next or previous event; from nothing, the next one after now. */
  moveSelection(step) {
    const order = this.order();
    if (!order.length) return;
    let index = order.findIndex((e) => e.key === this.state.selected);
    if (index === -1) {
      const now = new Date();
      index = order.findIndex((e) => e.end > now && e.startDay >= this.state.anchor);
      if (index === -1) index = step > 0 ? 0 : order.length - 1;
      else if (step < 0) index = Math.max(0, index - 1);
    } else {
      index = Math.min(order.length - 1, Math.max(0, index + step));
    }
    this.select(order[index].key);
  }

  selectedEvent() {
    return this.state.selected ? this.eventByKey(this.state.selected) : null;
  }

  // -------------------------------------------------------------------------
  // Event actions
  // -------------------------------------------------------------------------

  /** API payload of an event with the given changes. */
  payload(e, changes = {}) {
    const f = { ...e, ...changes };
    const allDay = f.all_day ?? f.allDay ?? false;
    return {
      calendar_id: f.calendar_id,
      title: f.title || '',
      location: f.location || '',
      notes: f.notes || '',
      all_day: allDay,
      ...wireTimes(allDay, f.start, f.end),
    };
  }

  /** Asks whether an edit of a recurring event applies to one occurrence or all. */
  chooseScope(verb = 'Change') {
    return new Promise((resolve) => {
      let answer = null;
      const pick = (scope) => { answer = scope; modal.close(); };
      const thisButton = h('button', { type: 'button', class: 'button primary', onclick: () => pick('this') }, 'This event');
      const modal = openModal({
        title: `${verb} a recurring event`,
        className: 'dialog',
        body: h('p', { class: 'dialog-message' }, 'Apply to this event only, or to every event of the series?'),
        footer: [
          h('button', { type: 'button', class: 'button', onclick: () => modal.close() }, 'Cancel'),
          h('button', { type: 'button', class: 'button', onclick: () => pick('all') }, 'All events'),
          thisButton,
        ],
        onClose: () => resolve(answer),
      });
      modal.el.addEventListener('keydown', (ev) => {
        if (ev.key === 'Enter') { ev.preventDefault(); pick('this'); }
        if (ev.key === 'a') { ev.preventDefault(); pick('all'); }
      });
      requestAnimationFrame(() => thisButton.focus());
    });
  }

  async createEvent(fields, { select = true, silent = false } = {}) {
    const body = this.payload(fields);
    if (fields.rrule) body.rrule = fields.rrule;
    if (!body.calendar_id) body.calendar_id = this.defaultCalendar()?.id;
    if (!body.calendar_id) {
      toast('Create a calendar first', { type: 'error' });
      return false;
    }
    const created = await this.run(() => api.events.create(body));
    if (!created) return false;
    prefs.set('lastCalendar', body.calendar_id);
    const e = hydrate(created);
    this.state.events.push(e);
    if (select) this.state.selected = e.key;
    this.render();
    if (select) this.viewEl?.reveal?.(e);
    if (!silent) toast('Event created', { type: 'success' });
    this.reload({ force: true });
    return e;
  }

  /** Saves the editor's fields; asks the scope for recurring events. */
  async saveEvent(e, fields) {
    let scope = 'all';
    if (e.recurring) {
      // A new rule or another calendar concerns the whole series.
      if (fields.rruleChanged || fields.calendar_id !== e.calendar_id) scope = 'all';
      else {
        scope = await this.chooseScope();
        if (!scope) return false;
      }
    }
    const body = { ...this.payload(fields), scope, recurrence_id: e.recurrence_id || '' };
    if (fields.rruleChanged) body.rrule = fields.rrule;
    const updated = await this.run(() => api.events.update(e.calendar_id, e.name, body));
    if (!updated) return false;
    prefs.set('lastCalendar', fields.calendar_id);
    this.state.selected = hydrate(updated).key;
    await this.reload({ force: true });
    return true;
  }

  /** Drop of a drag: moves the event, or copies it with Ctrl. */
  async dropEvent(e, { allDay, start, end }, copy) {
    if (copy) {
      await this.createEvent({ ...e, all_day: allDay, start, end, rrule: '' }, { silent: true });
      toast('Event copied', { type: 'success' });
      return;
    }
    let scope = 'all';
    if (e.recurring) {
      scope = await this.chooseScope('Move');
      if (!scope) return;
    }
    // Optimistic: show the move right away.
    const local = this.eventByKey(e.key);
    if (local) Object.assign(local, hydrate({ ...local, ...wireTimes(allDay, start, end), all_day: allDay }), { key: local.key });
    this.state.selected = e.key;
    this.render();
    const body = { ...this.payload(e, { all_day: allDay, start, end }), scope, recurrence_id: e.recurrence_id || '' };
    await this.run(() => api.events.update(e.calendar_id, e.name, body));
    await this.reload({ force: true });
  }

  /** Deletes an event, with Undo for single events. Returns true when done. */
  async deleteEvent(e) {
    if (e.read_only) return false;
    let scope = 'all';
    if (e.recurring) {
      scope = await this.chooseScope('Delete');
      if (!scope) return false;
    }
    const ok = await this.run(async () => {
      await api.events.remove(e.calendar_id, e.name, { scope, recurrenceId: e.recurrence_id || '' });
      return true;
    });
    if (!ok) return false;
    this.state.events = this.state.events.filter((x) => scope === 'this' ? x.key !== e.key : x.name !== e.name || x.calendar_id !== e.calendar_id);
    if (this.state.selected === e.key) this.state.selected = null;
    this.render();
    const undo = !e.recurring ? { label: 'Undo', run: () => this.createEvent({ ...e, rrule: '' }, { silent: true }) } : null;
    toast(scope === 'this' ? 'Occurrence deleted' : 'Event deleted', { action: undo });
    this.reload({ force: true });
    return true;
  }

  async duplicate(e) {
    await this.createEvent({ ...e, rrule: '' }, { silent: true });
    toast('Event duplicated', { type: 'success' });
  }

  copySelected() {
    const e = this.selectedEvent();
    if (!e) return;
    this.state.clipboard = e;
    toast(`Copied “${e.title || '(No title)'}”: p pastes it on the focused day`);
  }

  /** p: pastes the copied event on the focused day, at the same time. */
  async paste() {
    const c = this.state.clipboard;
    if (!c) {
      toast('Nothing to paste: copy an event with y first');
      return;
    }
    const day = this.state.anchor;
    let start;
    let end;
    if (c.all_day) {
      start = parseISO(day);
      end = parseISO(addDays(day, Math.round((c.end - c.start) / 86400000)));
    } else {
      start = parseISO(day);
      start.setHours(c.start.getHours(), c.start.getMinutes(), 0, 0);
      end = new Date(start.getTime() + (c.end - c.start));
    }
    const target = this.calendarOf(c.calendar_id)?.source_url ? this.defaultCalendar()?.id : c.calendar_id;
    await this.createEvent({ ...c, calendar_id: target, start, end, rrule: '' }, { silent: true });
  }

  /**
   * Keyboard moves (Shift+J/K/H/L, +/-): shown at once, saved in order. A
   * recurring event only moves this occurrence.
   */
  nudge({ minutes = 0, days = 0, resize = 0 }) {
    const e = this.selectedEvent();
    if (!e || e.read_only) return;
    let start = new Date(e.start);
    let end = new Date(e.end);
    if (days) {
      start.setDate(start.getDate() + days);
      end.setDate(end.getDate() + days);
    }
    if (minutes && !e.all_day) {
      start = new Date(start.getTime() + minutes * MINUTE);
      end = new Date(end.getTime() + minutes * MINUTE);
    }
    if (resize && !e.all_day) {
      end = new Date(Math.max(start.getTime() + 15 * MINUTE, end.getTime() + resize * MINUTE));
    }
    if (start.getTime() === e.start.getTime() && end.getTime() === e.end.getTime()) return;
    Object.assign(e, hydrate({ ...e, ...wireTimes(e.all_day, start, end) }), { key: e.key });
    if (days && this.days().includes(e.startDay)) this.state.anchor = e.startDay;
    this.render();
    this.viewEl?.reveal?.(e);
    const body = { ...this.payload(e), scope: e.recurring ? 'this' : 'all', recurrence_id: e.recurrence_id || '' };
    clearTimeout(this.nudgeReload);
    this.queue = this.queue
      .then(() => api.events.update(e.calendar_id, e.name, body))
      .catch((err) => toast(err.message, { type: 'error' }))
      .then(() => {
        this.nudgeReload = setTimeout(() => this.reload({ force: true }), 800);
      });
  }

  openEvent(e) {
    this.state.selected = e.key;
    this.render();
    openEditor(this, e);
  }

  openSelected() {
    const e = this.selectedEvent();
    if (e) openEditor(this, e);
  }

  /** a: a new event on the focused day, at the next round hour. */
  newEvent() {
    const day = this.state.anchor;
    const start = parseISO(day);
    const now = new Date();
    start.setHours(day === this.state.today ? Math.min(23, now.getHours() + 1) : 9, 0, 0, 0);
    openEditor(this, { allDay: false, all_day: false, start, end: new Date(start.getTime() + 60 * MINUTE), calendar_id: this.defaultCalendar()?.id }, { isNew: true });
  }

  /** The inline form for an event drawn on the grid. */
  openDraft(draft, anchor, onClose) {
    if (!this.writableCalendars().length) {
      onClose?.();
      toast('Create a calendar first', { type: 'error' });
      return;
    }
    openDraftForm(this, draft, anchor, onClose);
  }

  // -------------------------------------------------------------------------
  // Quick add, search, go to date
  // -------------------------------------------------------------------------

  openQuickAdd() {
    if (!this.writableCalendars().length) return toast('Create a calendar first', { type: 'error' });
    const input = h('input', { class: 'quick-input', placeholder: 'Sport tomorrow 18h-19h30 @Gym', autocomplete: 'off', maxLength: 500 });
    const chips = h('div', { class: 'quick-bar' });
    const calendar = h('select', { class: 'input', title: 'Calendar' },
      this.writableCalendars().map((c) => h('option', { value: c.id, selected: c.id === this.defaultCalendar()?.id }, c.name)));
    const preview = () => {
      const r = parseQuickAdd(input.value, { today: this.state.today });
      chips.replaceChildren(...r.tokens.map((t) => h('span', { class: ['token-chip', t.type] }, t.label)), calendar);
      return r;
    };
    input.addEventListener('input', preview);
    preview();
    const submit = async () => {
      const r = preview();
      if (!r.title) return;
      modal.close();
      const e = await this.createEvent({
        calendar_id: calendar.value, title: r.title, location: r.location, notes: '',
        all_day: r.allDay, start: r.start, end: r.end, rrule: r.rrule,
      }, { silent: true });
      if (e) {
        if (!this.days().includes(e.startDay)) this.setAnchor(e.startDay);
        toast(`Added on ${dayLabel(e.startDay)}${e.all_day ? '' : ` at ${hhmm(e.start)}`}`, { type: 'success' });
      }
    };
    input.addEventListener('keydown', (ev) => {
      if (ev.key === 'Enter') {
        ev.preventDefault();
        submit();
      }
    });
    const modal = openModal({ className: 'quick-add', body: [input, chips] });
  }

  openSearch() {
    const input = h('input', { class: 'picker-input', placeholder: 'Search events', autocomplete: 'off' });
    const list = h('div', { class: 'picker-list' }, h('div', { class: 'picker-empty' }, 'Type to search titles, places and notes'));
    let results = [];
    let active = 0;
    let timer = 0;
    const draw = () => {
      if (!results.length) {
        list.replaceChildren(h('div', { class: 'picker-empty' }, input.value.trim() ? 'No event found' : 'Type to search titles, places and notes'));
        return;
      }
      list.replaceChildren(...results.map((e, i) => {
        const cal = this.calendarOf(e.calendar_id);
        return h('button', {
          type: 'button',
          class: ['picker-item', i === active && 'active', `cal-${cal?.color || 'pine'}`],
          onclick: () => go(e),
        },
        h('span', { class: 'calendar-dot' }),
        h('span', { class: 'picker-label' }, e.title || '(No title)'),
        h('span', { class: 'picker-meta' }, `${dayLabel(e.startDay)}${e.startDay.slice(0, 4) !== this.state.today.slice(0, 4) ? ` ${e.startDay.slice(0, 4)}` : ''} · ${e.all_day ? 'all day' : hhmm(e.start)}`));
      }));
      list.children[active]?.scrollIntoView({ block: 'nearest' });
    };
    const go = async (e) => {
      modal.close();
      this.state.anchor = e.startDay;
      if (this.state.view === 'month' && !this.days().includes(e.startDay)) this.state.view = 'week';
      await this.reload({ force: true });
      this.select(e.key);
    };
    input.addEventListener('input', () => {
      clearTimeout(timer);
      timer = setTimeout(async () => {
        const q = input.value.trim();
        results = q ? ((await this.run(() => api.search(q))) || []).map(hydrate) : [];
        active = 0;
        draw();
      }, 150);
    });
    input.addEventListener('keydown', (ev) => {
      const down = ev.key === 'ArrowDown' || (ev.ctrlKey && ev.key === 'j');
      const up = ev.key === 'ArrowUp' || (ev.ctrlKey && ev.key === 'k');
      if (down || up) {
        ev.preventDefault();
        if (results.length) active = (active + (down ? 1 : -1) + results.length) % results.length;
        draw();
      } else if (ev.key === 'Enter' && results[active]) {
        ev.preventDefault();
        go(results[active]);
      }
    });
    const modal = openModal({
      className: 'picker',
      body: [h('div', { class: 'picker-search' }, icon('search'), input), list],
    });
  }

  /** g: jumps to a day typed in words ("15 nov", "next friday", "2027-01-04"). */
  goToDate() {
    const input = h('input', { class: 'quick-input', placeholder: 'Go to… (15 nov, friday, 2027-01-04)', autocomplete: 'off' });
    const hint = h('div', { class: 'quick-bar' });
    const parse = () => {
      const text = input.value.trim();
      if (!text) return null;
      const r = parseQuickAdd(text, { today: this.state.today });
      return r.title === text ? null : isoDate(r.start);
    };
    input.addEventListener('input', () => {
      const day = parse();
      hint.replaceChildren(day ? h('span', { class: 'token-chip date' }, longDayLabel(day)) : '');
    });
    input.addEventListener('keydown', (ev) => {
      if (ev.key !== 'Enter') return;
      ev.preventDefault();
      const day = parse();
      if (!day) return;
      modal.close();
      this.state.selected = null;
      this.setAnchor(day);
    });
    const modal = openModal({ className: 'quick-add', body: [input, hint] });
  }

  // -------------------------------------------------------------------------
  // Calendars
  // -------------------------------------------------------------------------

  async toggleCalendar(c) {
    c.hidden = !c.hidden;
    this.render();
    await this.run(() => api.calendars.update(c.id, { hidden: c.hidden }));
  }

  /** Dialog for a new or edited calendar (name, color, feed address). */
  calendarDialog({ title, calendar = null, subscription = false, submitLabel }) {
    return new Promise((resolve) => {
      const name = h('input', { class: 'input', value: calendar?.name || '', placeholder: subscription ? 'School timetable' : 'Personal', maxLength: 100 });
      const url = subscription && !calendar ? h('input', { class: 'input', placeholder: 'https://… or webcal://…', autocomplete: 'off' }) : null;
      let color = calendar?.color || COLORS[this.state.calendars.length % COLORS.length];
      const swatches = h('div', { class: 'swatches' });
      const drawSwatches = () => swatches.replaceChildren(...COLORS.map((c) => h('button', {
        type: 'button', class: ['swatch', `cal-${c}`, c === color && 'selected'], title: c,
        onclick: () => { color = c; drawSwatches(); },
      })));
      drawSwatches();
      const error = h('div', { class: 'form-error', hidden: true });
      let answer = null;
      const submit = () => {
        if (url && !url.value.trim()) {
          error.textContent = 'Paste the address of the iCalendar feed';
          error.hidden = false;
          return;
        }
        answer = { name: name.value.trim(), color, url: url?.value.trim() };
        modal.close();
      };
      const modal = openModal({
        title,
        className: 'dialog',
        body: [
          url ? h('label', { class: 'field' }, h('span', { class: 'field-label' }, 'Feed address'), url) : null,
          h('label', { class: 'field' }, h('span', { class: 'field-label' }, 'Name'), name),
          h('div', { class: 'field' }, h('span', { class: 'field-label' }, 'Color'), swatches),
          error,
        ],
        footer: [
          h('button', { type: 'button', class: 'button', onclick: () => modal.close() }, 'Cancel'),
          h('button', { type: 'button', class: 'button primary', onclick: submit }, submitLabel),
        ],
        onClose: () => resolve(answer),
      });
      modal.el.addEventListener('keydown', (ev) => {
        if (ev.key === 'Enter' && ev.target.tagName === 'INPUT') {
          ev.preventDefault();
          submit();
        }
      });
    });
  }

  async addCalendar(subscription) {
    const answer = await this.calendarDialog({
      title: subscription ? 'Subscribe to a calendar' : 'New calendar',
      subscription,
      submitLabel: subscription ? 'Subscribe' : 'Create',
    });
    if (!answer) return;
    if (subscription) toast('Downloading the calendar…');
    const created = await this.run(() => (subscription
      ? api.calendars.subscribe(answer.name, answer.url, answer.color)
      : api.calendars.create(answer.name || 'Calendar', answer.color)));
    if (!created) return;
    if (created.refresh_error) toast(`Subscribed, but the download failed: ${created.refresh_error}`, { type: 'error' });
    else toast(subscription ? 'Subscribed' : 'Calendar created', { type: 'success' });
    await this.reload({ force: true });
  }

  async editCalendar(c) {
    const answer = await this.calendarDialog({ title: 'Edit calendar', calendar: c, subscription: !!c.source_url, submitLabel: 'Save' });
    if (!answer) return;
    await this.run(() => api.calendars.update(c.id, { name: answer.name || c.name, color: answer.color }));
    await this.reload({ force: true });
  }

  async refreshCalendar(c) {
    const updated = await this.run(() => api.calendars.refresh(c.id));
    if (!updated) return;
    if (updated.refresh_error) toast(`Refresh failed: ${updated.refresh_error}`, { type: 'error' });
    else toast('Calendar refreshed', { type: 'success' });
    await this.reload({ force: true });
  }

  exportCalendar(c) {
    const link = h('a', { href: api.calendars.exportURL(c.id), download: `${c.name}.ics` });
    document.body.append(link);
    link.click();
    link.remove();
  }

  importCalendar(c) {
    const input = h('input', { type: 'file', accept: '.ics,text/calendar' });
    input.addEventListener('change', async () => {
      const file = input.files[0];
      if (!file) return;
      const result = await this.run(() => api.calendars.importFile(c.id, file));
      if (!result) return;
      toast(`${result.imported} event${result.imported === 1 ? '' : 's'} imported${result.skipped ? `, ${result.skipped} skipped` : ''}`, { type: 'success' });
      await this.reload({ force: true });
    });
    input.click();
  }

  async deleteCalendar(c) {
    const sub = !!c.source_url;
    const ok = await confirmDialog(sub ? 'Unsubscribe' : 'Delete calendar',
      sub ? `Remove “${c.name}” and its events from this app? The feed itself is not affected.`
        : `Delete “${c.name}” and all its events? This cannot be undone. Export it first to keep a copy.`,
      { okText: sub ? 'Unsubscribe' : 'Delete' });
    if (!ok) return;
    await this.run(() => api.calendars.remove(c.id));
    await this.reload({ force: true });
  }

  // -------------------------------------------------------------------------
  // Connect other apps (CalDAV)
  // -------------------------------------------------------------------------

  async openConnect() {
    const info = await this.run(() => api.info());
    if (!info) return;
    const host = info.server.replace(/^https?:\/\//, '').replace(/\/$/, '');
    const copyField = (value) => {
      const input = h('input', { class: 'input', value, readOnly: true });
      return h('div', { class: 'copy-field' }, input, h('button', {
        type: 'button', class: 'icon-button', title: 'Copy',
        onclick: async () => {
          try {
            await navigator.clipboard.writeText(value);
            toast('Copied', { type: 'success' });
          } catch {
            input.select();
          }
        },
      }, icon('copy')));
    };
    openModal({
      title: 'Connect a calendar app',
      className: 'dialog',
      body: h('div', { class: 'connect-steps' },
        h('p', null, 'Your calendars are served over CalDAV: phone and desktop apps read and edit them live, and changes show up everywhere.'),
        h('div', { class: 'field' }, h('span', { class: 'field-label' }, 'Server'), copyField(host)),
        h('div', { class: 'field' }, h('span', { class: 'field-label' }, 'CalDAV address'), copyField(info.caldav_url)),
        h('p', null, h('strong', null, 'iPhone / iPad: '), 'Settings › Apps › Calendar › Calendar Accounts › Add Account › Other › Add CalDAV Account. Server: the address above, user name: anything, password: your Calendar password.'),
        h('p', null, h('strong', null, 'macOS: '), 'Calendar › Settings › Accounts › + › Other CalDAV Account, type “Manual”, same details.'),
        h('p', null, h('strong', null, 'Android: '), 'DAVx⁵ › Login with URL and user name, with the CalDAV address.'),
        h('p', null, h('strong', null, 'Thunderbird: '), 'New calendar › On the network, with the CalDAV address.'),
        info.no_auth ? h('p', { class: 'form-error' }, 'This instance has no password: set one before exposing it to the internet.') : null),
    });
  }

  // -------------------------------------------------------------------------
  // Help
  // -------------------------------------------------------------------------

  openHelp() {
    if (topModal()?.el.classList.contains('help')) return topModal().close();
    const row = (keys, label) => h('div', { class: 'help-row' }, h('span', null, label), h('span', { class: 'help-keys' }, keys.map((k) => h('kbd', null, k))));
    openModal({
      title: 'Keyboard shortcuts',
      className: 'help',
      body: h('div', { class: 'help-grid' },
        h('section', null,
          h('h3', { class: 'field-label' }, 'Navigation'),
          row(['h', 'l'], 'Previous / next period'),
          row(['t'], 'Today'),
          row(['d', 'w', 'm'], 'Day / week / month view'),
          row(['g'], 'Go to a date'),
          row(['j', 'k'], 'Next / previous event'),
          row(['/'], 'Search'),
          row(['?'], 'This help'),
          row(['Esc'], 'Close, leave a field, clear selection'),
          h('h3', { class: 'field-label' }, 'Mouse'),
          row(['Drag'], 'Create, move or resize'),
          row(['Ctrl', 'Drag'], 'Copy the event')),
        h('section', null,
          h('h3', { class: 'field-label' }, 'Events'),
          row(['a'], 'New event on the focused day'),
          row(['q'], 'Quick add in one line'),
          row(['Enter'], 'Open the selected event'),
          row(['x'], 'Delete'),
          row(['y', 'p'], 'Copy / paste on the focused day'),
          row(['J', 'K'], 'Move 15 minutes later / earlier'),
          row(['L', 'H'], 'Move one day later / earlier'),
          row(['+', '-'], 'Lengthen / shorten by 15 minutes'),
          h('h3', { class: 'field-label' }, 'Editor'),
          row(['Ctrl', 'Enter'], 'Save'))),
    });
  }

  // -------------------------------------------------------------------------
  // Authentication
  // -------------------------------------------------------------------------

  passwordForm({ title, intro, fields, submitLabel, dismissible, onSubmit }) {
    const error = h('div', { class: 'form-error', hidden: true });
    const inputs = fields.map((f) => h('input', {
      type: 'password', class: 'input', placeholder: f.placeholder, autocomplete: f.autocomplete, attrs: { 'aria-label': f.label },
    }));
    const submit = h('button', { type: 'submit', class: 'button primary wide' }, submitLabel);
    const form = h('form', { class: 'auth-form' },
      intro ? h('p', { class: 'dialog-message' }, intro) : null,
      error,
      fields.map((f, i) => h('label', { class: 'field' }, h('span', { class: 'field-label' }, f.label), inputs[i])),
      submit);
    let modal;
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      error.hidden = true;
      submit.disabled = true;
      try {
        const message = await onSubmit(inputs.map((i) => i.value));
        if (message) {
          error.textContent = message;
          error.hidden = false;
        } else {
          modal.close();
        }
      } catch (err) {
        error.textContent = err.message;
        error.hidden = false;
      } finally {
        submit.disabled = false;
      }
    });
    modal = openModal({ title, body: form, className: 'dialog auth', dismissible });
    return modal;
  }

  showSetup() {
    closeAllModals({ keepUndismissible: false });
    this.state.auth.authenticated = false;
    this.passwordForm({
      title: 'Welcome to Calendar',
      intro: 'Choose a master password to protect this instance. Calendar apps (CalDAV) use it too. You can change it later.',
      fields: [
        { label: 'Master password', placeholder: 'At least 4 characters', autocomplete: 'new-password' },
        { label: 'Confirm password', placeholder: 'Repeat the password', autocomplete: 'new-password' },
      ],
      submitLabel: 'Set password and continue',
      dismissible: false,
      onSubmit: async ([password, confirm]) => {
        if (password.length < 4) return 'The password must be at least 4 characters long.';
        if (password !== confirm) return 'The passwords do not match.';
        await api.auth.setup(password);
        this.afterLogin();
        return null;
      },
    });
  }

  showLogin() {
    if (topModal()?.el.classList.contains('auth')) return;
    closeAllModals({ keepUndismissible: false });
    closePopover();
    this.state.auth.authenticated = false;
    this.passwordForm({
      title: 'Calendar is locked',
      intro: 'Enter your master password to unlock.',
      fields: [{ label: 'Password', placeholder: 'Master password', autocomplete: 'current-password' }],
      submitLabel: 'Unlock',
      dismissible: false,
      onSubmit: async ([password]) => {
        if (!password) return 'Enter your password.';
        await api.auth.login(password);
        this.afterLogin();
        return null;
      },
    });
  }

  async afterLogin() {
    this.state.auth = { ...this.state.auth, has_password: true, authenticated: true };
    await this.loadAll();
  }

  changePassword() {
    this.passwordForm({
      title: 'Change password',
      intro: 'Calendar apps connected over CalDAV will ask for the new password.',
      fields: [
        { label: 'Current password', placeholder: 'Current master password', autocomplete: 'current-password' },
        { label: 'New password', placeholder: 'At least 4 characters', autocomplete: 'new-password' },
        { label: 'Confirm new password', placeholder: 'Repeat the new password', autocomplete: 'new-password' },
      ],
      submitLabel: 'Update password',
      dismissible: true,
      onSubmit: async ([current, next, confirm]) => {
        if (next.length < 4) return 'The new password must be at least 4 characters long.';
        if (next !== confirm) return 'The new passwords do not match.';
        await api.auth.changePassword(current, next);
        toast('Password updated. Other devices were signed out.', { type: 'success' });
        return null;
      },
    });
  }

  async lock() {
    await this.run(() => api.auth.logout());
    this.showLogin();
  }

  showFatal(err) {
    const retry = h('button', { type: 'button', class: 'button primary', onclick: () => { modal.close(); this.boot(); } }, 'Retry');
    const modal = openModal({
      title: 'Calendar is unavailable',
      className: 'dialog',
      dismissible: false,
      body: h('p', { class: 'dialog-message' }, err.message),
      footer: retry,
    });
  }

  // -------------------------------------------------------------------------
  // Static chrome (header, sidebar, status bar)
  // -------------------------------------------------------------------------

  openSidebar() {
    $('sidebar').classList.add('open');
    $('sidebar-backdrop').classList.add('open');
    document.body.classList.add('sidebar-open');
  }

  closeSidebar() {
    $('sidebar').classList.remove('open');
    $('sidebar-backdrop').classList.remove('open');
    document.body.classList.remove('sidebar-open');
  }

  bindChrome() {
    const setIcon = (id, name) => $(id).prepend(icon(name));
    setIcon('settings-menu', 'more');
    setIcon('add-calendar', 'plus');
    setIcon('menu-toggle', 'menu');
    setIcon('prev-button', 'chevronLeft');
    setIcon('next-button', 'chevronRight');
    setIcon('fab', 'plus');
    $('connect-button').append(icon('phone'), h('span', null, 'Connect a calendar app'));

    $('menu-toggle').addEventListener('click', () => this.openSidebar());
    $('sidebar-backdrop').addEventListener('click', () => this.closeSidebar());
    $('prev-button').addEventListener('click', () => this.shift(-1));
    $('next-button').addEventListener('click', () => this.shift(1));
    $('today-button').addEventListener('click', () => this.goToday());
    for (const b of $('view-switch').querySelectorAll('button')) {
      b.addEventListener('click', () => this.setView(b.dataset.view));
    }
    $('add-calendar').addEventListener('click', () => openMenu($('add-calendar'), [
      { label: 'New calendar', icon: 'calendar', run: () => this.addCalendar(false) },
      { label: 'Subscribe to a feed (.ics)', icon: 'link', run: () => this.addCalendar(true) },
    ], { title: 'Add' }));
    $('settings-menu').addEventListener('click', () => openMenu($('settings-menu'), [
      { label: 'Connect a calendar app', icon: 'phone', run: () => this.openConnect() },
      { label: 'Keyboard shortcuts', icon: 'search', hint: '?', run: () => this.openHelp() },
      ...(this.state.auth.no_auth ? [] : [
        'separator',
        { label: 'Change password', icon: 'key', run: () => this.changePassword() },
        { label: 'Lock', icon: 'lock', run: () => this.lock() },
      ]),
    ], { title: 'Settings' }));
    $('connect-button').addEventListener('click', () => this.openConnect());
    $('fab').addEventListener('click', () => this.openQuickAdd());
    $('status-help').addEventListener('click', () => this.openHelp());
  }
}

const app = new App();
window.addEventListener('DOMContentLoaded', () => app.start());

