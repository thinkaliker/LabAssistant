// Service worker for the installed app (PWA). It caches nothing: the dashboard is a live view of
// the manager, and a cached copy would outlive a manager update ("Update & restart manager") and
// keep serving the old code. All it does is answer a page load the manager can't serve — down,
// or mid-restart — with a small "unreachable" page that reloads itself once the manager is back,
// instead of the browser's own offline error inside the app window.
//
// Only page loads (navigations) come through here. API calls, the SSE stream and assets are left
// alone, so they reach the manager exactly as they would without a service worker.
//
// Served from the web root so its scope covers the whole dashboard.

// A 502-504 is a reverse proxy in front of the manager (`tailscale serve`, say) reporting it down.
const PROXY_DOWN = new Set([502, 503, 504]);

const UNREACHABLE = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>LabAssistant</title>
<style>
  :root { color-scheme: light; --bg: #fff; --fg: #0a0a0a; --muted: #737373; --border: #e5e5e5; }
  :root.dark { color-scheme: dark; --bg: #0a0a0a; --fg: #fafafa; --muted: #a1a1a1; --border: #262626; }
  body { margin: 0; min-height: 100dvh; display: grid; place-items: center; padding: 1.5rem;
    box-sizing: border-box; background: var(--bg); color: var(--fg);
    font: 14px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; }
  main { max-width: 24rem; text-align: center; }
  h1 { font-size: 1.125rem; margin: .75rem 0 .25rem; }
  p { margin: 0 0 1rem; color: var(--muted); }
  button { font: inherit; padding: .4rem .9rem; border-radius: .5rem; border: 1px solid var(--border);
    background: transparent; color: inherit; cursor: pointer; }
</style>
<script>
  // Same theme choice as the dashboard (stored by its head script), so this doesn't flash white.
  let mode = 'auto';
  try { mode = localStorage.getItem('theme') || 'auto'; } catch (e) { /* auto */ }
  if (mode === 'dark' || (mode === 'auto' && matchMedia('(prefers-color-scheme: dark)').matches)) {
    document.documentElement.classList.add('dark');
  }
</script>
</head>
<body>
<main>
  <svg viewBox="0 0 24 24" width="40" height="40" aria-hidden="true">
    <path d="M9.5 9H14.5L20 19H4z" fill="#00d1b2"/>
    <path d="M9.5 3h5M9.5 3v6L4 19h16L14.5 9V3" fill="none" stroke="#0f9d8f" stroke-width="1.8"
      stroke-linejoin="round" stroke-linecap="round"/>
  </svg>
  <h1>Can't reach the manager</h1>
  <p>It may be restarting or updating. This page reloads by itself once the manager answers.</p>
  <button type="button" onclick="location.reload()">Retry now</button>
</main>
<script>
  setInterval(async () => {
    try {
      const r = await fetch('/api/v1/auth/session', { cache: 'no-store' });
      if (r.ok) location.reload();
    } catch (e) { /* still down */ }
  }, 3000);
</script>
</body>
</html>`;

const unreachable = () => new Response(UNREACHABLE, {
  status: 503,
  headers: { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' },
});

// A new version takes over at once rather than waiting for every app window to close: with
// nothing cached, there is no old state for it to conflict with.
self.addEventListener('install', () => self.skipWaiting());

self.addEventListener('activate', (event) => {
  event.waitUntil((async () => {
    // Navigation preload starts the page request while this worker is still starting up, so
    // routing page loads through it costs no extra latency.
    if (self.registration.navigationPreload) await self.registration.navigationPreload.enable();
    await self.clients.claim();
  })());
});

self.addEventListener('fetch', (event) => {
  if (event.request.mode !== 'navigate') return;
  event.respondWith((async () => {
    try {
      const res = (await event.preloadResponse) || (await fetch(event.request));
      return PROXY_DOWN.has(res.status) ? unreachable() : res;
    } catch (e) {
      return unreachable();
    }
  })());
});
