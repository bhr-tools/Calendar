// Vim-style keyboard control. One window-level handler decides, in order:
//   1. Escape closes the topmost popover or dialog, or clears the selection;
//   2. while a dialog is open, other keys belong to it;
//   3. while typing in a field (INSERT mode), keys belong to the field;
//   4. otherwise keys are commands.
// Components that handle a key themselves call preventDefault(), and this
// handler then ignores it.

import { escapeTopLayer, topModal, isPopoverOpen } from './ui/overlay.js';
import { isTextInput } from './dom.js';
import { SNAP } from './views/grid.js';

export function initKeys(app) {
  const updateMode = () => app.renderStatus();
  document.addEventListener('focusin', updateMode);
  document.addEventListener('focusout', () => setTimeout(updateMode, 0));

  window.addEventListener('keydown', (e) => {
    if (e.defaultPrevented || e.isComposing) return;

    if (e.key === 'Escape') {
      if (escapeTopLayer()) {
        e.preventDefault();
        return;
      }
      if (isTextInput(document.activeElement)) {
        e.preventDefault();
        document.activeElement.blur();
        return;
      }
    }
    if (topModal() || isPopoverOpen() || isTextInput(e.target)) return;
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    if (command(e.key)) e.preventDefault();
  });

  function command(key) {
    switch (key) {
      case 'h': case 'ArrowLeft': app.shift(-1); return true;
      case 'l': case 'ArrowRight': app.shift(1); return true;
      case 'j': case 'ArrowDown': app.moveSelection(1); return true;
      case 'k': case 'ArrowUp': app.moveSelection(-1); return true;
      case 't': app.goToday(); return true;
      case 'd': app.setView('day'); return true;
      case 'w': app.setView('week'); return true;
      case 'm': app.setView('month'); return true;
      case 'g': app.goToDate(); return true;
      case 'J': app.nudge({ minutes: SNAP }); return true;
      case 'K': app.nudge({ minutes: -SNAP }); return true;
      case 'L': app.nudge({ days: 1 }); return true;
      case 'H': app.nudge({ days: -1 }); return true;
      case '+': case '=': app.nudge({ resize: SNAP }); return true;
      case '-': app.nudge({ resize: -SNAP }); return true;
      case 'Enter': case 'e': app.openSelected(); return true;
      case 'a': app.newEvent(); return true;
      case 'q': app.openQuickAdd(); return true;
      case 'x': case 'Delete': {
        const ev = app.selectedEvent();
        if (ev) app.deleteEvent(ev);
        return true;
      }
      case 'y': app.copySelected(); return true;
      case 'p': app.paste(); return true;
      case '/': app.openSearch(); return true;
      case '?': app.openHelp(); return true;
      case 'Escape': app.select(null); return true;
    }
    return false;
  }
}
