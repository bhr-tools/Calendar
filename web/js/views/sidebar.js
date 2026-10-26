// Sidebar: the calendars and subscriptions, each with its color and a menu.
// Clicking a calendar shows or hides its events.

import { h, icon } from '../dom.js';
import { openMenu } from '../ui/overlay.js';

export function renderSidebar(app, list) {
  list.replaceChildren(...app.state.calendars.map((c) => {
    const more = h('button', { type: 'button', class: 'icon-button', title: 'Calendar options' }, icon('more'));
    more.addEventListener('click', (ev) => {
      ev.stopPropagation();
      openMenu(more, [
        { label: 'Edit', icon: 'edit', run: () => app.editCalendar(c) },
        c.source_url ? { label: 'Refresh now', icon: 'refresh', run: () => app.refreshCalendar(c) } : null,
        { label: 'Export (.ics)', icon: 'download', run: () => app.exportCalendar(c) },
        { label: 'Secret address (Google…)', icon: 'link', run: () => app.shareCalendar(c) },
        c.source_url ? null : { label: 'Import (.ics)', icon: 'upload', run: () => app.importCalendar(c) },
        'separator',
        { label: c.source_url ? 'Unsubscribe' : 'Delete', icon: 'trash', danger: true, run: () => app.deleteCalendar(c) },
      ].filter(Boolean));
    });
    return h('div', {
      class: ['calendar-row', `cal-${c.color}`, c.hidden && 'hidden-cal'],
      title: c.hidden ? 'Show this calendar' : 'Hide this calendar',
      onclick: () => app.toggleCalendar(c),
    },
    h('span', { class: 'calendar-dot' }),
    h('span', { class: 'calendar-name' }, c.name),
    c.refresh_error ? h('span', { class: 'calendar-error', title: `Last refresh failed: ${c.refresh_error}` }, '!') : null,
    c.source_url ? h('span', { class: 'icon-link', title: 'Subscription (read-only)' }, icon('link')) : null,
    more);
  }));
}
