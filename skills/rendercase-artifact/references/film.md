# Films: animated, scored artifacts

The film player in `assets/film-player/` plays a timed sequence of animated
scenes with a synthesized score, in the browser, with no video file. A film is a
directory of data and optional code beside a fixed player. Build it with
`scripts/build_film.py` and publish the ZIP like any artifact.

Choose the least code that gives the look you need:

1. **Data only.** Each scene in `scenes.json` names a stock `type` and holds its
   text in `data`. With no `score.js`, the default score follows the cut list.
   Use this for recurring films such as a weekly recap: write a new
   `scenes.json` each time, and no scene code.
2. **Your own style.** Add a `palette`, `fonts/`, a `base.css` overriding the
   stock classes, and `logo.html`. Stock scenes use the logo on the title and
   end cards and as a corner bug.
3. **Your own scene types.** Register them once in `shared.js` with
   `kit.type(name, (data, scene) => ({ init, render }))` and keep using them
   from `scenes.json`. Keep a house style's `shared.js`, `base.css`, fonts and
   logo in the project that owns the brand, and copy them into each film.
4. **Hand-written scenes and score.** `scene-<id>.js` and `score.js` against the
   kit API below, for one-off sequences the types can't express.

These mix freely: one film can use stock, custom-type and hand-written scenes.

## Build and publish

```sh
python3 scripts/build_film.py /tmp/film-src /tmp/film-out
```

The script validates the film, rejects unknown files, writes `/tmp/film-out/`
and `/tmp/film-out.zip`, and prints the running time. It exits non-zero with the
offending field on invalid input, and refuses to overwrite an existing output.
Check privacy as for any artifact. Then publish the ZIP following
[publishing.md](publishing.md): use one new artifact per film unless the user
wants successive films as versions of one artifact.

Preview locally by serving the output directory over HTTP. Opening the file
directly also works, because all film data is in `film-data.js`. Append
`#t=12.5` to the URL to open at a given time.

## Film directory

| File | Contents |
| --- | --- |
| `scenes.json` | Required. The manifest below. |
| `base.css` | Optional. Loaded after the stock theme; overrides any of it. |
| `shared.js` | Optional. Runs once before any scene; define globals and `kit.type`s. |
| `scene-<id>.js` | One per scene without a `type`; calls `kit.scene('<id>', …)`. |
| `score.js` | Optional. Calls `kit.score(fn)`; replaces the default score. |
| `<name>.html` | Markup fragments, read with `kit.module('<name>.html')`. |
| `fonts/<Stem>.<otf/ttf/woff/woff2>` | Each becomes `@font-face` family `<Stem>`. |

Each script runs in its own scope, so scenes may all declare the same top-level
names, such as `const { seg, eo } = kit;`. Fonts are inlined as `data:` URIs:
Rendercase frames artifacts in a sandbox where separate font files are blocked.
The content security policy also blocks web fonts and all network access, so
bundle any font you need.

### scenes.json

| Field | Meaning |
| --- | --- |
| `width`, `height`, `fps` | Stage size in CSS pixels, and the frame rate used to seed randomness. |
| `title` | Page title. Defaults to the first scene's name. |
| `background` | Color around the stage when the frame is letterboxed. |
| `palette` | Up to four colors `[ground, main, second, accent]`, set as CSS variables on the stage. |
| `fonts` | `{sans, mono, display}`. Each is a family name or `{family, or}`, exposed as `--sans`, `--mono`, `--display` with system fallbacks. |
| `music` | Tunes the default score: `tempo`, `key` (`"D"`, `"Bb"`), `mode` (`major`/`minor`), `drums`. `false` makes the film silent. |
| `scenes` | Up to 60 scenes played back to back, at most 600 s in total. |

Each scene has `id` (lower-case letters, digits and `-`, starting with a letter,
unique), `name` (timeline label), optional `type` and `data`, and `dur` in
seconds. `dur` is required for hand-written scenes. For typed scenes, the build
derives it from the amount of text when it is omitted: 5 to 14 seconds, enough
to read after the entrance. A `code` scene also lasts until its `typed` text has
finished typing, plus a short hold. Other fields, such as `desc`, are ignored.

### Stock scene types

All text fields are non-empty strings and are rendered as text, never HTML. The
build rejects missing required fields, unknown fields and wrong types, so a
mistake fails the build rather than the film.

| Type | `data` fields (required in bold) |
| --- | --- |
| `title` | `kicker`, **`title`**, `subtitle`, `stats` (`[{value, label}]`, up to four) |
| `statement` | `kicker`, **`title`**, `sub`, `tag` |
| `stat` | `kicker`, **`value`**, **`label`**, `detail`, `tag`. A numeric value counts up. |
| `list` | **`title`**, **`items`** (strings or `{name, desc, hot}`), `tag` |
| `chips` | **`title`**, **`chips`** (strings), `hot` (index to highlight), `sub`, `tag` |
| `code` | **`title`**, `sub`, `tag`, `file`, `before`, **`typed`**, `after`. `typed` is typed out, highlighted. |
| `quote` | **`text`**, `attribution` |
| `end` | **`title`**, `sub` |

## Kit API

### Scenes

```js
kit.scene(id, {
  init(kit) { /* build DOM once inside kit.stage; may be async */ },
  render(t, kit) { /* set every varying style from t alone */ },
});
```

`t` is seconds since the scene's start. `render` must be synchronous and a pure
function of `t`: the player calls it for any time, in any order, when scrubbing
or seeking. Scenes cut rather than crossfade. The first scene renders at
`t = 0` as the thumbnail, so start its entrances below zero (for example
`-0.6`) if frame 0 should show content. `this` carries what `init` built over to
`render`. `kit.stage` is the scene's own full-size box with no background; the
stock `.ground` class paints one. `kit.data` is the scene's `data`. CSS
transitions and animations are disabled inside the stage, and `Math.random` is
seeded per frame.

| Call | Behavior |
| --- | --- |
| `kit.h(html)` | Parse markup and return its first element. |
| `kit.module(name)` | A bundled `.html` file's text, or `null`. |
| `kit.seg(t, a, b)` | Progress of `t` across `[a, b]`, clamped to 0..1. |
| `kit.lerp(a, b, p)` | Linear mix. |
| `kit.eo(x)`, `kit.eio(x)` | Ease-out and ease-in-out cubic on 0..1. |
| `kit.eob(x, k = 1.4)` | Back-out ease with overshoot `k`. |
| `kit.TextFx.fadeFromRight(el, t, t0)` | Splits `el`'s text into words once; word *i* fades in and slides 26 px from the right over 0.5 s from `t0 + 0.11·i`. |
| `kit.typist(text, t0, cps)` | `{count(t), at(t), end}`: characters typed by `t`, the typed prefix, and the finish time. |
| `kit.type(name, make)` | Register a reusable scene type; `make(data, scene)` returns `{init, render}`. |

### Score

```js
kit.score(function (S, T) { … });
```

`T[id]` is each scene's start in seconds. The function runs once at load and
records events, which the player synthesizes with Web Audio during playback.
Velocities run 0–127; pitches are names like `'F#4'` or MIDI numbers.

| Call | Behavior |
| --- | --- |
| `S.tempo(bpm, {t0})` | Set the beat grid. `S.toBeat(t)` converts seconds to beats. |
| `S.program(track, patch, {pan, gain, rev, bright})` | Patches: `pad`, `ep`, `bass`, `marimba`, `pluck`. `gain` is in dB. |
| `S.note(track, pitch, {beat, beats} or {t, dur}, vel)` | One note; `vel` may also be given in the timing object. |
| `S.chord(track, pitches, timing, vel)` | Several notes at once. |
| `S.pattern(track, 'x.xX', {from, repeat, pitch, vel, step = 0.5})` | `x` hit, `X` accent, `o` ghost, `.` rest. `from` and `step` are in beats; `pitch` cycles. |
| `S.drums({kick, sidestick, hat, shaker}, {from, repeat, vel})` | Step patterns per drum. |
| `S.drum(name, {t, vel})` | One hit: the drums above, `snare` or `crash`. |
| `S.run(track, pitches, {t, step, vel, cresc})` | A quick run, `cresc` added to the velocity per note. |
| `S.fx(name, t, {dur, vel})` | `pop`, `whoosh`, `roll`, `click`, `chime`, `revswell` (swells into `t`). |
| `S.ticks(times, {vel})` | A keystroke tick at each time. |
| `S.master({gain, fadeOut})` | Output level in dB, and a fade over the last `fadeOut` seconds. |

## Player behavior and limits

- Controls: play/pause, a scrubber divided into chapters by scene, the current
  chapter name, time, and a sound toggle. Sound starts off because browsers
  only allow audio after a click. Space toggles playback; on the focused
  scrubber, arrows step 2 s, Page Up/Down jump between chapters, and Home/End
  go to either end.
- When playback ends, the last frame stays on screen.
- `window.film` exposes `duration`, `time`, `seek(t)`, `play()` and `pause()`
  for automation.
- There is no MP4 export. A headless browser could record one by driving
  `window.film.seek` frame by frame.
