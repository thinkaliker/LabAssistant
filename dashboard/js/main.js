// Assembles the Alpine component (index.html's x-data="app()") from domain modules. Each module
// exports a plain object of state + methods; Object.assign merges them into one object. `this`
// inside every method resolves to Alpine's reactive component at call time, so the split is
// transparent — methods in one module freely call methods/fields declared in another.
//
// Loaded as an ES module (deferred), so it runs before DOMContentLoaded and window.app is
// defined by the time the deferred Alpine bundle initializes.
import { core } from './core.js';
import { hosts } from './hosts.js';
import { services } from './services.js';
import { composeform } from './composeform.js';
import { envedit } from './envedit.js';
import { updates } from './updates.js';
import { jobs } from './jobs.js';
import { actions } from './actions.js';
import { scheduler } from './scheduler.js';
import { misc } from './misc.js';
import { ansi } from './ansi.js';

// app() is merged once per page load, so the nested-object references shared by Object.assign
// are harmless (one component instance). Alpine wraps the result in its reactive proxy.
window.app = function app() {
  return Object.assign({}, core, hosts, services, composeform, envedit, updates, jobs, actions, scheduler, misc, ansi);
};

// Installable app (PWA): register the service worker (/sw.js; it caches nothing, see there).
// Browsers only offer service workers, and app install, on HTTPS or localhost; over plain HTTP
// navigator.serviceWorker is absent and the dashboard simply stays a web page. Registered after
// load so it never competes with the page's own requests.
if ('serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch((e) => console.warn('service worker not registered:', e));
  });
}

// x-dialog="expr" drives a native <dialog>: showModal() while expr is truthy, close() once it turns
// falsy. It never writes state back. Esc and backdrop clicks end in the dialog's own 'close' event,
// which each dialog maps to its state with @close (and can veto beforehand with @cancel).
//
// expr === 'inline' opens it with show() instead: not modal, so the page around it stays usable
// and Esc / backdrop don't apply (the compose editor's docked-panel layout). show() still moves
// focus into the dialog, onto its first button; a panel beside the page shouldn't take focus from
// it, so it goes back to where it was (or nowhere, as before the panel opened). Which way it opens
// is read when it opens; an open dialog keeps its kind.
//
// A backdrop click only counts when the press also started on the backdrop: drag-selecting text
// out of an input and releasing past the panel's edge otherwise lands a click on the <dialog>
// itself and would throw the form away.
//
// Registered on alpine:init, which fires after this module has run: Alpine loads after it.
document.addEventListener('alpine:init', () => {
  window.Alpine.directive('dialog', (el, { expression }, { effect, evaluateLater, cleanup }) => {
    const isOpen = evaluateLater(expression);
    effect(() => isOpen((open) => {
      if (open && !el.open) {
        if (open !== 'inline') { el.showModal(); return; }
        const prev = document.activeElement;
        el.show();
        if (prev && prev !== document.body && !el.contains(prev)) prev.focus({ preventScroll: true });
        else if (el.contains(document.activeElement)) document.activeElement.blur();
      }
      else if (!open && el.open) el.close();
    }));
    let pressedBackdrop = false;
    const onPointerDown = (e) => { pressedBackdrop = e.target === el; };
    const onClick = (e) => {
      if (!pressedBackdrop || e.target !== el || !el.matches(':modal')) return;
      // requestClose() fires 'cancel' first, so a dialog vetoing Esc vetoes this too.
      if (el.requestClose) el.requestClose(); else el.close();
    };
    el.addEventListener('pointerdown', onPointerDown);
    el.addEventListener('click', onClick);
    cleanup(() => {
      el.removeEventListener('pointerdown', onPointerDown);
      el.removeEventListener('click', onClick);
    });
  });
});
