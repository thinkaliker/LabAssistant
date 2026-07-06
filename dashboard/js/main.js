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
import { updates } from './updates.js';
import { jobs } from './jobs.js';
import { actions } from './actions.js';
import { scheduler } from './scheduler.js';
import { misc } from './misc.js';

// app() is merged once per page load, so the nested-object references shared by Object.assign
// are harmless (one component instance). Alpine wraps the result in its reactive proxy.
window.app = function app() {
  return Object.assign({}, core, hosts, services, updates, jobs, actions, scheduler, misc);
};
