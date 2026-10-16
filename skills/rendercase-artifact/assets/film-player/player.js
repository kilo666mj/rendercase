// Rendercase film player: the kit API scenes and scores are written against.
// Scenes render as pure functions of time; the score is recorded as events and
// synthesized with Web Audio in sync with playback. See references/film.md.
"use strict";
(function () {
  const FILM = window.__FILM__;
  const defs = {}, types = {};
  let scoreFn = null;
  const clamp01 = (x) => (x < 0 ? 0 : x > 1 ? 1 : x);

  const kit = (window.kit = {
    stage: null,
    data: null,
    h(html) {
      const t = document.createElement("template");
      t.innerHTML = String(html).trim();
      return t.content.firstElementChild;
    },
    module: (name) => (Object.prototype.hasOwnProperty.call(FILM.modules, name) ? FILM.modules[name] : null),
    seg: (t, a, b) => (b === a ? (t >= b ? 1 : 0) : clamp01((t - a) / (b - a))),
    lerp: (a, b, p) => a + (b - a) * p,
    eo: (x) => 1 - (1 - clamp01(x)) ** 3,
    eio: (x) => ((x = clamp01(x)), x < 0.5 ? 4 * x ** 3 : 1 - (-2 * x + 2) ** 3 / 2),
    eob: (x, k = 1.4) => ((x = clamp01(x)), 1 + (k + 1) * (x - 1) ** 3 + k * (x - 1) ** 2),
    typist(text, t0, cps) {
      const count = (t) => Math.max(0, Math.min(text.length, Math.floor((t - t0) * cps)));
      return { count, at: (t) => text.slice(0, count(t)), end: t0 + text.length / cps };
    },
    TextFx: {
      fadeFromRight(el, t, t0) {
        if (!el._words) {
          el._words = [];
          const parts = el.textContent.split(/(\s+)/);
          el.textContent = "";
          for (const w of parts) {
            if (!w.trim()) { el.append(w); continue; }
            const s = document.createElement("span");
            s.style.display = "inline-block";
            s.textContent = w;
            el.append(s);
            el._words.push(s);
          }
        }
        el._words.forEach((s, i) => {
          const p = kit.eo(kit.seg(t, t0 + 0.11 * i, t0 + 0.11 * i + 0.5));
          s.style.opacity = p;
          s.style.transform = "translateX(" + kit.lerp(26, 0, p) + "px)";
        });
      },
    },
    scene(id, def) { defs[id] = def; },
    // A reusable scene kind: make(data, scene) returns {init, render} for each scene of that type.
    type(name, make) { types[name] = make; },
    score(fn) { scoreFn = fn; },
    defaultScore: null,
  });

  // ---------- score recording ----------
  const NOTE = { C: 0, D: 2, E: 4, F: 5, G: 7, A: 9, B: 11 };
  function midi(p) {
    if (typeof p === "number") return p;
    const m = /^([A-G])(#|b)?(-?\d)$/.exec(p);
    if (!m) throw new Error("bad pitch " + p);
    return 12 * (+m[3] + 1) + NOTE[m[1]] + (m[2] === "#" ? 1 : m[2] === "b" ? -1 : 0);
  }
  function record(fn, T) {
    const ev = [], programs = {}, master = { gain: 0, fadeOut: 0 }, grid = { bpm: 120, t0: 0 };
    const B = () => 60 / grid.bpm, bt = (b) => grid.t0 + b * B();
    const when = (o) => (o.t != null ? [o.t, o.dur ?? B()] : [bt(o.beat ?? 0), (o.beats ?? 1) * B()]);
    const steps = (str, from, repeat, step, vel, hit) => {
      for (let r = 0; r < repeat; r++)
        [...str].forEach((c, i) => {
          if (c === ".") return;
          hit(bt(from + (r * str.length + i) * step), c === "X" ? Math.min(127, vel * 1.3) : c === "o" ? vel * 0.45 : vel);
        });
    };
    const S = {
      tempo(bpm, o = {}) { grid.bpm = bpm; grid.t0 = o.t0 ?? 0; },
      toBeat: (t) => (t - grid.t0) / B(),
      program(track, patch, o = {}) { programs[track] = { patch, ...o }; },
      note(track, pitch, o = {}, vel) {
        const [t, dur] = when(o);
        ev.push({ k: "note", track, m: midi(pitch), t, dur, vel: vel ?? o.vel ?? 80 });
      },
      chord(track, pitches, o = {}, vel) { pitches.forEach((p) => S.note(track, p, o, vel)); },
      pattern(track, str, { from = 0, repeat = 1, pitch = ["C4"], vel = 80, step = 0.5 } = {}) {
        let n = 0;
        steps(str, from, repeat, step, vel, (t, v) =>
          ev.push({ k: "note", track, m: midi(pitch[n++ % pitch.length]), t, dur: step * B() * 0.9, vel: v }));
      },
      drums(map, { from = 0, repeat = 1, vel = 80, step = 0.5 } = {}) {
        for (const [name, str] of Object.entries(map)) steps(str, from, repeat, step, vel, (t, v) => ev.push({ k: "drum", name, t, vel: v }));
      },
      drum(name, { t, vel = 80 } = {}) { ev.push({ k: "drum", name, t, vel }); },
      run(track, pitches, { t, step = 0.1, vel = 80, cresc = 0 } = {}) {
        pitches.forEach((p, i) => ev.push({ k: "note", track, m: midi(p), t: t + i * step, dur: step * 1.6, vel: Math.min(127, vel + cresc * i) }));
      },
      fx(name, t, { dur = 0.6, vel = 70 } = {}) { ev.push({ k: "fx", name, t, dur, vel }); },
      ticks(times, { vel = 40 } = {}) { times.forEach((t) => ev.push({ k: "fx", name: "tick", t, dur: 0.02, vel })); },
      master(o) { Object.assign(master, o); },
    };
    fn(S, T);
    ev.sort((a, b) => a.t - b.t);
    return { ev, programs, master };
  }

  // ---------- synthesis ----------
  const db = (d) => 10 ** (d / 20);
  let ctx = null, noise = null, impulse = null;
  function audio() {
    if (ctx) return ctx;
    ctx = new (window.AudioContext || window.webkitAudioContext)();
    noise = ctx.createBuffer(1, ctx.sampleRate * 2, ctx.sampleRate);
    const d = noise.getChannelData(0);
    for (let i = 0; i < d.length; i++) d[i] = Math.random() * 2 - 1;
    impulse = ctx.createBuffer(2, ctx.sampleRate * 2.5, ctx.sampleRate);
    for (let c = 0; c < 2; c++) {
      const ch = impulse.getChannelData(c);
      for (let i = 0; i < ch.length; i++) ch[i] = (Math.random() * 2 - 1) * (1 - i / ch.length) ** 3;
    }
    return ctx;
  }
  function env(g, t, peak, a, hold, r) {
    g.gain.setValueAtTime(0.0001, t);
    g.gain.exponentialRampToValueAtTime(Math.max(peak, 0.0002), t + a);
    g.gain.setValueAtTime(Math.max(peak, 0.0002), t + a + hold);
    g.gain.exponentialRampToValueAtTime(0.0001, t + a + hold + r);
    return t + a + hold + r;
  }
  function osc(type, f, t, end, dest, detune = 0) {
    const o = ctx.createOscillator();
    o.type = type; o.frequency.value = f; o.detune.value = detune;
    o.connect(dest); o.start(t); o.stop(end + 0.05);
    return o;
  }
  function burst(t, dur, filter, f, q, dest) {
    const s = ctx.createBufferSource(), b = ctx.createBiquadFilter();
    s.buffer = noise; b.type = filter; b.frequency.value = f; b.Q.value = q;
    s.connect(b); b.connect(dest); s.start(t, Math.random()); s.stop(t + dur + 0.05);
    return b;
  }
  function lowpass(f, dest) {
    const b = ctx.createBiquadFilter();
    b.type = "lowpass"; b.frequency.value = f; b.connect(dest);
    return b;
  }
  const PATCHES = {
    pad(f, t, dur, v, out, br) {
      const g = ctx.createGain(); g.connect(out);
      const lp = lowpass(900 * br, g), end = env(g, t, v * 0.5, 0.5, Math.max(0, dur - 0.5), 1.2);
      osc("sawtooth", f, t, end, lp, -7); osc("sawtooth", f, t, end, lp, 7);
    },
    ep(f, t, dur, v, out, br) {
      const g = ctx.createGain(); g.connect(out);
      const end = env(g, t, v, 0.005, 0.02, Math.min(2.5, dur + 0.6));
      osc("sine", f, t, end, g);
      const h = ctx.createGain(); h.connect(out);
      osc("sine", f * 2, t, env(h, t, v * 0.25 * br, 0.003, 0, 0.35), h);
    },
    bass(f, t, dur, v, out, br) {
      const g = ctx.createGain(); g.connect(out);
      const lp = lowpass(500 * br, g), end = env(g, t, v * 1.1, 0.01, Math.max(0, dur - 0.08), 0.12);
      osc("triangle", f, t, end, lp); osc("sine", f, t, end, g);
    },
    marimba(f, t, dur, v, out, br) {
      const g = ctx.createGain(); g.connect(out);
      osc("sine", f, t, env(g, t, v, 0.003, 0, 0.5), g);
      const h = ctx.createGain(); h.connect(out);
      osc("sine", f * 4, t, env(h, t, v * 0.2 * br, 0.002, 0, 0.08), h);
    },
    pluck(f, t, dur, v, out, br) {
      const g = ctx.createGain(); g.connect(out);
      const lp = lowpass(3500 * br, g);
      lp.frequency.setValueAtTime(3500 * br, t);
      lp.frequency.exponentialRampToValueAtTime(300, t + 0.3);
      osc("square", f, t, env(g, t, v * 0.6, 0.003, 0, 0.4), lp);
    },
  };
  PATCHES.default = PATCHES.ep;

  function drum(name, t, v, out) {
    const g = ctx.createGain(); g.connect(out);
    if (name === "kick") {
      const o = osc("sine", 140, t, env(g, t, v * 1.6, 0.002, 0.02, 0.3), g);
      o.frequency.setValueAtTime(140, t); o.frequency.exponentialRampToValueAtTime(42, t + 0.14);
    } else if (name === "sidestick") burst(t, env(g, t, v * 0.9, 0.001, 0, 0.06), "bandpass", 1900, 4, g);
    else if (name === "hat") burst(t, env(g, t, v * 0.35, 0.001, 0, 0.05), "highpass", 7500, 0.7, g);
    else if (name === "shaker") burst(t, env(g, t, v * 0.25, 0.02, 0, 0.07), "bandpass", 6000, 1.2, g);
    else if (name === "crash") burst(t, env(g, t, v * 0.5, 0.002, 0, 1.8), "highpass", 4500, 0.5, g);
    else if (name === "snare") burst(t, env(g, t, v * 0.7, 0.001, 0, 0.18), "bandpass", 2200, 0.8, g);
  }
  function fx(name, t, dur, v, out) {
    const g = ctx.createGain(); g.connect(out);
    if (name === "pop") {
      const o = osc("sine", 520, t, env(g, t, v * 0.7, 0.003, 0, 0.09), g);
      o.frequency.setValueAtTime(520, t); o.frequency.exponentialRampToValueAtTime(980, t + 0.06);
    } else if (name === "whoosh") {
      g.gain.setValueAtTime(0.0001, t);
      g.gain.exponentialRampToValueAtTime(v * 0.6, t + dur * 0.6);
      g.gain.exponentialRampToValueAtTime(0.0001, t + dur);
      const b = burst(t, dur, "bandpass", 300, 1.5, g);
      b.frequency.setValueAtTime(300, t); b.frequency.exponentialRampToValueAtTime(3200, t + dur);
    } else if (name === "roll") {
      for (let x = 0; x < dur; x += 0.045) drum("snare", t + x, v * (0.3 + 0.6 * (x / dur)), out);
    } else if (name === "click") burst(t, env(g, t, v * 0.8, 0.0005, 0, 0.015), "highpass", 2500, 0.7, g);
    else if (name === "tick") burst(t, env(g, t, v * 0.9, 0.0005, 0, 0.012), "highpass", 3500, 0.7, g);
    else if (name === "chime") {
      const end = env(g, t, v * 0.4, 0.002, 0, 1.4);
      [1, 2.76, 5.4].forEach((r) => osc("sine", 1568 * r, t, end, g));
    } else if (name === "revswell") {
      const s = Math.max(0.05, t - dur);
      g.gain.setValueAtTime(0.0001, s);
      g.gain.exponentialRampToValueAtTime(v * 0.7, t);
      g.gain.setValueAtTime(0.0001, t + 0.01);
      burst(s, dur, "bandpass", 1200, 0.6, g);
    }
  }

  // One bus per playback run; pausing or seeking drops it and starts a fresh one.
  function Bus(score, offset, from) {
    const master = ctx.createGain(), rev = ctx.createConvolver(), tracks = {};
    rev.buffer = impulse; rev.connect(master); master.connect(ctx.destination);
    const lvl = db(score.master.gain || 0), fade = score.master.fadeOut || 0, endT = total;
    const now = ctx.currentTime;
    master.gain.setValueAtTime(0.0001, now);
    master.gain.exponentialRampToValueAtTime(lvl, now + 0.05);
    if (fade > 0 && from < endT) {
      const fs = Math.max(from, endT - fade);
      master.gain.setValueAtTime(lvl, offset + fs + 0.05);
      master.gain.exponentialRampToValueAtTime(0.0001, offset + endT);
    }
    const track = (name) => {
      if (tracks[name]) return tracks[name];
      const p = score.programs[name] || {};
      const g = ctx.createGain(), pan = ctx.createStereoPanner();
      g.gain.value = db(p.gain || 0) * 0.35; pan.pan.value = p.pan || 0;
      g.connect(pan); pan.connect(master);
      if (p.rev) { const s = ctx.createGain(); s.gain.value = p.rev; g.connect(s); s.connect(rev); }
      return (tracks[name] = { out: g, patch: PATCHES[p.patch] || PATCHES.default, br: p.bright || 1 });
    };
    const perc = ctx.createGain(); perc.gain.value = 0.35; perc.connect(master);
    let i = 0;
    // Notes already sounding at the seek point join in for their remaining length.
    const pending = score.ev.filter((e) => e.t < from && e.k === "note" && e.t + e.dur > from + 0.05);
    while (i < score.ev.length && score.ev[i].t < from) i++;
    const play = (e, at) => {
      const v = (e.vel / 127) ** 1.5;
      if (e.k === "note") {
        const tr = track(e.track), f = 440 * 2 ** ((e.m - 69) / 12);
        tr.patch(f, at, Math.max(0.05, e.dur - (at - offset - e.t)), v, tr.out, tr.br);
      } else if (e.k === "drum") drum(e.name, at, v, perc);
      else fx(e.name, at, e.dur, v, perc);
    };
    pending.forEach((e) => play(e, ctx.currentTime + 0.02));
    const pump = () => {
      const horizon = ctx.currentTime - offset + 0.5;
      while (i < score.ev.length && score.ev[i].t < horizon) {
        const e = score.ev[i++];
        play(e, Math.max(ctx.currentTime + 0.01, offset + e.t));
      }
    };
    pump();
    const timer = setInterval(pump, 120);
    return {
      stop() {
        clearInterval(timer);
        const n = ctx.currentTime;
        master.gain.cancelScheduledValues(n);
        master.gain.setValueAtTime(master.gain.value, n);
        master.gain.linearRampToValueAtTime(0, n + 0.06);
        setTimeout(() => master.disconnect(), 200);
      },
    };
  }

  // ---------- playback ----------
  let scenes = [], total = 0, score = null, t = 0, playing = false, muted = true, bus = null, clock0 = 0, shown = -1;
  const ui = {};

  function frame(time) {
    t = Math.max(0, Math.min(total, time));
    let i = scenes.findIndex((s) => t < s.start + s.dur);
    if (i < 0) i = scenes.length - 1;
    const s = scenes[i];
    if (i !== shown) {
      if (shown >= 0) scenes[shown].box.style.display = "none";
      s.box.style.display = "";
      shown = i;
    }
    // Determinism: Math.random is seeded per frame, so any frame renders the same way twice.
    let seed = (Math.round(t * (FILM.manifest.fps || 60)) + 1) >>> 0;
    Math.random = () => {
      seed = (seed + 0x6d2b79f5) >>> 0;
      let x = Math.imul(seed ^ (seed >>> 15), 1 | seed);
      x = (x + Math.imul(x ^ (x >>> 7), 61 | x)) ^ x;
      return ((x ^ (x >>> 14)) >>> 0) / 4294967296;
    };
    s.def.render.call(s.def, t - s.start, s.kit);
    ui.fill.style.width = (t / total) * 100 + "%";
    ui.bar.setAttribute("aria-valuenow", t.toFixed(1));
    ui.bar.setAttribute("aria-valuetext", fmt(t) + ", " + s.name);
    ui.time.textContent = fmt(t) + " / " + fmt(total);
    ui.chapter.textContent = s.name;
    ui.chapters.forEach((c, j) => c.classList.toggle("on", j === i));
  }
  const fmt = (x) => Math.floor(x / 60) + ":" + String(Math.floor(x % 60)).padStart(2, "0");

  function sound(on) {
    if (bus) { bus.stop(); bus = null; }
    if (on && score && playing && !muted) {
      audio().resume();
      bus = Bus(score, ctx.currentTime - t, t);
    }
  }
  function setPlaying(p) {
    if (p && t >= total) frame(0);
    playing = p;
    clock0 = performance.now() - t * 1000;
    ui.play.textContent = p ? "❚❚" : "▶";
    ui.play.setAttribute("aria-label", p ? "Pause" : "Play");
    sound(p);
  }
  function seek(x) {
    frame(x);
    clock0 = performance.now() - t * 1000;
    if (playing) sound(true);
  }
  function tick() {
    if (playing) {
      const now = (performance.now() - clock0) / 1000;
      frame(now);
      // The last frame stays on screen when playback ends.
      if (now >= total) setPlaying(false);
    }
    requestAnimationFrame(tick);
  }

  function chrome(root) {
    root.insertAdjacentHTML("beforeend",
      '<div class="fp-controls"><button class="fp-play" aria-label="Play">▶</button>' +
      '<div class="fp-bar" role="slider" tabindex="0" aria-label="Timeline" aria-valuemin="0"><div class="fp-chapters"></div><div class="fp-fill"></div></div>' +
      '<span class="fp-chapter"></span><span class="fp-time"></span>' +
      '<button class="fp-mute" aria-label="Turn sound on" aria-pressed="false">🔇</button></div>');
    Object.assign(ui, {
      play: root.querySelector(".fp-play"), bar: root.querySelector(".fp-bar"), fill: root.querySelector(".fp-fill"),
      time: root.querySelector(".fp-time"), chapter: root.querySelector(".fp-chapter"), mute: root.querySelector(".fp-mute"),
    });
    ui.bar.setAttribute("aria-valuemax", total.toFixed(1));
    ui.chapters = scenes.map((s) => {
      const c = document.createElement("span");
      c.style.flexGrow = s.dur;
      c.title = s.name;
      root.querySelector(".fp-chapters").append(c);
      return c;
    });
    if (!score) ui.mute.hidden = true;
    ui.play.onclick = () => setPlaying(!playing);
    ui.mute.onclick = () => {
      muted = !muted;
      ui.mute.textContent = muted ? "🔇" : "🔊";
      ui.mute.setAttribute("aria-pressed", String(!muted));
      ui.mute.setAttribute("aria-label", muted ? "Turn sound on" : "Turn sound off");
      sound(true);
    };
    const at = (e) => {
      const r = ui.bar.getBoundingClientRect();
      seek(clamp01((e.clientX - r.left) / r.width) * total);
    };
    ui.bar.onpointerdown = (e) => {
      ui.bar.setPointerCapture(e.pointerId);
      at(e);
      ui.bar.onpointermove = at;
    };
    ui.bar.onpointerup = () => { ui.bar.onpointermove = null; };
    ui.bar.onkeydown = (e) => {
      const starts = scenes.map((s) => s.start), cur = scenes.findIndex((s) => t < s.start + s.dur);
      const moves = {
        ArrowLeft: t - 2, ArrowRight: t + 2, Home: 0, End: total,
        PageUp: starts[Math.max(0, (t - starts[cur] < 0.5 ? cur - 1 : cur))], PageDown: starts[cur + 1] ?? total,
      };
      if (e.key in moves) { e.preventDefault(); seek(moves[e.key]); }
    };
    document.addEventListener("keydown", (e) => {
      if (e.key === " " && !/^(BUTTON|INPUT)$/.test(e.target.tagName) && e.target !== ui.bar) { e.preventDefault(); setPlaying(!playing); }
    });
  }

  function fit(wrap, stage, m) {
    const box = wrap.getBoundingClientRect();
    const s = Math.min(box.width / m.width, box.height / m.height);
    stage.style.transform = "translate(" + (box.width - m.width * s) / 2 + "px," + (box.height - m.height * s) / 2 + "px) scale(" + s + ")";
  }

  async function boot() {
    const m = FILM.manifest;
    const root = document.getElementById("film"), wrap = root.querySelector(".fp-wrap"), stage = document.getElementById("stage");
    document.title = m.title || m.scenes[0].name;
    document.documentElement.style.setProperty("--fp-bg", m.background || "#000");
    stage.style.width = m.width + "px";
    stage.style.height = m.height + "px";
    const STACK = { sans: "system-ui, sans-serif", mono: "ui-monospace, monospace", display: "var(--sans, system-ui, sans-serif)" };
    for (const slot of ["sans", "mono", "display"]) {
      const v = m.fonts && m.fonts[slot];
      if (!v) continue;
      const names = (typeof v === "string" ? [v] : [v.family, v.or]).filter(Boolean);
      stage.style.setProperty("--" + slot, names.map((n) => JSON.stringify(n)).join(", ") + ", " + STACK[slot]);
    }
    if (m.palette) ["ground", "main", "second", "accent"].forEach((k, i) => m.palette[i] && stage.style.setProperty("--" + k, m.palette[i]));
    const T = {};
    let at = 0;
    scenes = m.scenes.map((s) => {
      const box = document.createElement("div");
      box.className = "fp-scene";
      box.style.display = "none";
      stage.append(box);
      const sk = Object.create(kit, { stage: { value: box }, data: { value: s.data || {} } });
      let def = defs[s.id];
      if (!def && s.type) {
        if (!types[s.type]) throw new Error("unknown scene type " + s.type);
        def = types[s.type](s.data || {}, s);
      }
      if (!def) throw new Error("scene " + s.id + " was never registered");
      T[s.id] = at;
      const r = { ...s, start: at, box, def, kit: sk };
      at += s.dur;
      return r;
    });
    total = at;
    await Promise.all(scenes.map((s) => s.def.init && s.def.init.call(s.def, s.kit)));
    const fn = scoreFn || (m.music !== false && kit.defaultScore ? (S, TT) => kit.defaultScore(S, TT, m) : null);
    if (fn) score = record(fn, T);
    chrome(root);
    new ResizeObserver(() => fit(wrap, stage, m)).observe(wrap);
    const start = parseFloat((location.hash.match(/t=([\d.]+)/) || [])[1]);
    frame(isFinite(start) ? start : 0);
    requestAnimationFrame(tick);
    // A small control surface for automation, e.g. a future headless recording step.
    window.film = { duration: total, seek, play: () => setPlaying(true), pause: () => setPlaying(false), get time() { return t; } };
  }
  window.addEventListener("DOMContentLoaded", () =>
    boot().catch((e) => {
      document.getElementById("film").insertAdjacentHTML("beforeend", '<p class="fp-error"></p>');
      document.querySelector(".fp-error").textContent = "This film could not load: " + e.message;
      throw e;
    }));
})();
