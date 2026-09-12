# Hide the search bar on mobile when scrolling down on the video player page

## Summary

On phones the sticky topbar covers a large share of the watch page: at `max-width: 780px`
the search form becomes its own full-width second row, so the header eats roughly 7.4rem
of viewport while the user is trying to read a description or comments. Collapse the
search-bar row when the user scrolls down on the video player page, and restore it as soon
as they scroll back up. Brand, queue, and settings stay pinned; desktop and every other
page are untouched.

## Current behavior

- **Topbar markup:** `frontend/src/App.svelte:3477-3534`. `<main class="app-shell" class:watch-mode={videoID}>`
  switches the whole shell into watch mode whenever a `/videos/:id` route is active
  (`videoIDFromPath`, `App.svelte:2161`). The `<header class="topbar">` holds
  `.brand-cluster`, the `<form class="search-form">` (`:3483-3487`, input `#site-search`,
  a Search button, `submitSearch` at `:2154`), and `.top-actions` (quick-queue +
  settings). The watch page itself renders through `routes/WatchRoute.svelte`
  (`App.svelte:3680`).
- **Sticky header:** `frontend/src/style.css:75-90` — `.topbar` is `position: sticky;
  top: 0; z-index: 20; height: 4.5rem`. It never leaves the viewport on any page.
- **Watch mode:** `frontend/src/style.css:1826-1835` — `.watch-mode` collapses the shell
  grid to a single column and hides the sidebar (`.watch-mode .sidebar { display: none }`,
  specificity 0,2,0 — this wins over the mobile `.sidebar { display: block }` at 0,1,0,
  so the sidebar never shows on the watch page at any width).
- **Mobile layout:** `frontend/src/style.css:2868-2999` (`@media (max-width: 780px)`).
  The topbar becomes a two-row grid: row 1 = brand + `.top-actions`
  (`grid-template-columns: 1fr auto`, `height: auto`, `:2874-2886`), and
  `.search-form { grid-column: 1 / -1; grid-row: 2; order: 3 }`
  (`:2888-2892`) — the search bar is a full-width row under the brand row. The mobile
  sidebar is sticky at `top: 7.25rem` (`:2893-2899`), a value that bakes in the two-row
  header — relevant only as a constraint if a future change ever collapses a non-watch
  page header. The `max-width: 1180px` block (`:2815-2865`) keeps the search form as the
  middle column of a three-column topbar; `max-width: 520px` (`:3002-3022`) does not
  touch the search row.
- **Scroll handling:** `App.svelte` has no window scroll listener today. It registers
  `popstate`/`focus`/`visibilitychange`/two `pointerdown`/two `keydown` listeners
  (`App.svelte:302-305`) and removes them in `onDestroy` (`:306`). Navigation always
  scrolls to top (`setRoute`, `App.svelte:345` and `:352`).
- **Tests:** `frontend/playwright.config.js` runs the same suite on `desktop`
  (1280×800) and `mobile` (Pixel 5, 393×851 — exercises the `≤780px` layout). The
  viewport-skip precedent is `frontend/e2e/smoke.spec.js:433-436`; scroll-manipulation
  patterns (spacer element, `window.scrollY` polling) exist at `smoke.spec.js:897-931`;
  the watch-page fixture is `/videos/e2e-video` (`smoke.spec.js:613`).

## Problem

The watch page is the one place a phone user spends sustained time scrolling (description,
comments, up-next), and the sticky header consumes the most vertical space exactly there:
~7.4rem, of which ~3.8rem is the search bar. The search bar has no purpose while a video
is on screen, yet it stays pinned over the content. Scrolling down to read comments halves
that waste; scrolling back up restores search instantly — the standard mobile video-app
convention (YouTube, etc.).

## Design

### 1. State and scroll trigger

Add one piece of state in `App.svelte`, defaulting to visible:

```js
let topbarSearchCollapsed = false;
let lastScrollY = window.scrollY;
```

Register a **passive** `window` scroll listener next to the existing listeners
(`App.svelte:302-305`), and remove it in `onDestroy` alongside the others. The handler:

1. **Exits early unless both** conditions hold: a `/videos/:id` route is active
   (`videoID` truthy ⇒ watch mode) **and** `window.matchMedia('(max-width: 780px)').matches`.
   Desktop never enters the collapsed state at all, so it needs no reset dance.
2. **Accumulates direction before toggling:** `delta = window.scrollY - lastScrollY`;
   only cross the ±10px dead zone, then toggle `topbarSearchCollapsed` and remember the
   crossing point (`lastScrollY = window.scrollY` at each toggle). This is hysteresis,
   not a per-event flip: tiny scrolls and momentum wobble must not flap the header.
3. **Forces visible at the top:** `window.scrollY <= 0` always sets
   `topbarSearchCollapsed = false`.
4. **Never hides while the user is typing:** if `document.activeElement ===
   document.getElementById('site-search')`, force visible. Losing the input mid-search
   would be hostile; the guard also covers the on-screen-keyboard case where the scroll
   position does not move but a wheel/keyboard event may still fire.

Decision (F1): collapse **only the `.search-form` row** (the thing called the search
bar). Brand + `.top-actions` stay pinned, so queue/settings remain one tap away and the
quick-queue panel (positioned inside `.top-actions`, row 1) is unaffected by the collapse.
Hiding the entire topbar is a superset variant — see Notes.

Initial state must be `false`; also reset to `false` on every route change so a
scrolled-in collapsed header can never leak onto another page: in `setRoute`
(`App.svelte:344-353`), in `handlePopState` (`:290-294`), and alongside the
`routeKey`-change load in the reactive block. `setRoute` scrolls to top anyway, and the
`scrollY <= 0` rule makes the reset idempotent.

### 2. Render and CSS

Two small, local changes:

- `App.svelte:3478`: `<header class="topbar" data-testid="topbar" class:search-collapsed={topbarSearchCollapsed}>`
  (new stable testid for e2e; the collapse class lives on the header so the e2e can
  assert one element).
- In the `max-width: 780px` block (`style.css:2868`), after the existing `.search-form`
  rules:

```css
.watch-mode .topbar.search-collapsed .search-form {
  display: none;
}
```

Decision (F2): prune the row with `display: none`, not a visibility/opacity fade. The
point of the feature is reclaiming viewport height, which means the header must shrink,
and a visual fade without a height collapse reclaims nothing. `display: none` also drops
the input and button from tab order and the accessibility tree for the duration — the
correct end-state for a deliberately hidden control; no `aria-hidden` juggling needed.
The collapse is **instant** (F3): the grid row height cannot be transitioned, so animating
the topbar's shrink requires restructuring the header markup (a slide-out wrapper) — a
nice follow-up, not v1. The Media Query is the only scope gate: the watch-mode +
collapsed-state class combination cannot apply above 780px because `.search-form` is a
grid column there, not a row.

No behavioral change is needed for cinema mode (`.watch-page.cinema-mode`): the player
goes full-bleed, but the header stays sticky above it and the same
scroll-down/collapse rule applies, which is exactly the desired behavior while
watching.

### 3. Keyboard and edge behavior

- Keyboard scrolling (space / arrows) fires the same scroll events, so direction logic
  holds; scrolling back up restores the bar. This is standard and acceptable — note it
  only.
- Fullscreen video (mobile browsers detach the player) hides the whole page chrome
  natively; when the user exits fullscreen the scroll listener state is unchanged and
  correct because it is purely `scrollY`-derived.
- A scrolled-away browser tab or a `popstate` restore that lands mid-page: reset via
  `handlePopState` (F1) plus the `scrollY`-derived state — no stale collapsed header.

## Requirements

1. Add `topbarSearchCollapsed` state and a passive, `onDestroy`-cleaned window scroll
   listener in `App.svelte`, gated on watch mode + `matchMedia('(max-width: 780px)')`.
2. Implement the ±10px hysteresis toggle (scroll down past the zone → collapse; scroll up
   past the zone → restore; `scrollY <= 0` always visible).
3. Never collapse while `#site-search` has focus.
4. Reset the state to visible on route change (`setRoute`, `handlePopState`,
   `routeKey`-change), so the collapse never leaks to another page or route.
5. Add `data-testid="topbar"` and `class:search-collapsed` on the `<header>`; add the
   single collapsing CSS rule under `@media (max-width: 780px)`.
6. Add a mobile-project e2e test (viewport-skipped for desktop, mirroring
   `smoke.spec.js:433-436`) covering collapse on scroll-down, restore on scroll-up,
   force-visible at the top, and the focus guard.

## Acceptance Criteria

- On the watch page (`/videos/:id`) with a ≤780px viewport, scrolling down past the dead
  zone collapses the search row: the input and Search button are not visible, not
  interactive, and not in the tab order; the topbar shrinks to the brand/actions row and
  stays sticky.
- Scrolling back up restores the search row; at `scrollY <= 0` it is always visible.
- The search row does **not** collapse while the user is focused in the search input.
- The collapse never happens on any non-watch page, at widths above 780px, or on the
  desktop Playwright project — the desktop watch page is pixel-identical to today.
- Route changes (in-app navigation, back/forward) reset the collapse state; no test or
  manual flow can observe a collapsed header on another page.
- The mobile e2e test passes in the `mobile` Playwright project and the full existing
  desktop + mobile suite stays green.
- No other header control (quick-queue, settings, brand) changes position, size, or
  behavior.

## Testing

- `frontend/e2e/smoke.spec.js`: one new test, skipped when `(viewport?.width ?? 0) > 780`
  (invert the existing desktop-skip at `:433-436`). Flow: `page.goto('/videos/e2e-video')`
  (`:613`), assert the topbar does not have `search-collapsed`; scroll down past the zone
  (spacer + `window.scrollTo`/`scrollIntoViewIfNeeded`, following `:897-931`), poll until
  `data-testid="topbar"` has `search-collapsed`; scroll back to top, poll until it does
  not; refocus `#site-search`, scroll down again, poll that it still does not collapse.
  The fixture page must be tall enough to scroll — append a spacer like the existing
  scroll test does if comments alone are not enough.
- Manual: rebuild and restart the local instance per `AGENTS.md` (Yummle local instance
  section), open a video on a phone-sized DevTools viewport, verify collapse/restore,
  focus guard, route-change reset, and that desktop widths and `/search`, `/`, `/channels`
  pages are unchanged. Also sanity-check with a real device if available (momentum
  scrolling is the main real-world difference from Playwright).

## Non-goals

- Hiding the **entire** topbar (brand + actions) — this collapse is search-row-only; see
  Notes for the superset variant.
- Applying the behavior to any page other than the watch route, or to the `≤1180px`
  desktop-narrow layout where the search bar is a grid column.
- Animating/sliding the collapse (F3 follow-up), or adding a user preference/setting for
  toggleable auto-hide.
- Any change to `WatchRoute.svelte` or the player.

## Notes

- The mobile sidebar's sticky `top: 7.25rem` (`style.css:2893-2899`) assumes a two-row
  header. It is inert on the watch page (sidebar hidden), so this feature does not
  conflict with it — but if the whole-topbar variant is ever extended to other pages,
  that value breaks and must become `4.5rem` (or dynamic). Recorded here so the trap is
  not re-discovered.
- F1 alternative — collapse the whole topbar: strictly more viewport reclaimed and closer
  to mobile YouTube, but the quick-queue panel and settings button leave the screen with
  it, and the collapse CSS/semantics change (the header must stay reachable for the panel
  to open). Recommended against for v1: the request names the search bar, and the
  brand/actions row is small and genuinely useful while reading comments.
- F2 note: `display: none` chosen over `visibility: hidden; opacity: 0` (which would keep
  the header at two rows and reclaim nothing) and over `position: fixed` re-parenting.
  If a later pass wants a smooth shrink, the clean way is a wrapper that animates
  `grid-template-rows`/`max-height` — out of scope here.
- Dead-zone value (±10px) matches common mobile auto-hide implementations; it is a
  named constant so it can be tuned without touching the logic.
- The settle-on-direction behavior (reveal on any scroll-up) is deliberate: mid-page it
  is more useful than "reveal only near the top", and cheaper than a "reveal after N ms
  of idle" timer.

## Review references

- `frontend/src/App.svelte:302-306` (listener registration + `onDestroy`),
  `:344-353` (`setRoute` scroll-to-top), `:3477-3534` (topbar markup, `:3483-3487`
  search form), `:2154-2159` (`submitSearch`), `:2161-2166` (`videoIDFromPath`),
  `:3680` (`WatchRoute` usage)
- `frontend/src/routes/WatchRoute.svelte` (watch page — untouched)
- `frontend/src/style.css:75-90` (`.topbar` sticky), `:173-198` (`.search-form`),
  `:1826-1835` (`.watch-mode`), `:2815-2865` (`max-width: 1180px`),
  `:2868-2999` (`max-width: 780px`; `.topbar` `:2874`, `.search-form` `:2888-2892`,
  `.sidebar` `top: 7.25rem` `:2893-2899`), `:3002-3022` (`max-width: 520px`)
- `frontend/playwright.config.js` (desktop/mobile projects); `frontend/e2e/smoke.spec.js:433-436`
  (viewport skip precedent), `:613` (`/videos/e2e-video` fixture), `:897-931`
  (scroll assertions)