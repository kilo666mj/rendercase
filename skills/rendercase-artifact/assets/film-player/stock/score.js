// Default score for films without a score.js: a four-chord groove in the film's
// key that fits the cut list, with a whoosh into each scene and a final hit.
// scenes.json "music": {tempo, key, mode} tunes it; "music": false turns it off.
(function () {
  const KEYS = { C: 0, "C#": 1, Db: 1, D: 2, "D#": 3, Eb: 3, E: 4, F: 5, "F#": 6, Gb: 6, G: 7, "G#": 8, Ab: 8, A: 9, "A#": 10, Bb: 10, B: 11 };
  const MAJOR = [0, 2, 4, 5, 7, 9, 11], MINOR = [0, 2, 3, 5, 7, 8, 10];
  kit.defaultScore = function (S, T, film) {
    const music = film.music || {};
    const bpm = music.tempo || 104, B = 60 / bpm;
    const scale = music.mode === "minor" ? MINOR : MAJOR;
    const tonic = 48 + (KEYS[music.key] ?? 2); // MIDI, octave 3
    const at = (deg, oct = 0) => tonic + 12 * (oct + Math.floor(deg / 7)) + scale[((deg % 7) + 7) % 7];
    const triad = (d, oct) => [at(d, oct), at(d + 2, oct), at(d + 4, oct)];
    // I-V-vi-IV in major, i-VI-III-VII in minor.
    const prog = music.mode === "minor" ? [0, 5, 2, 6] : [0, 4, 5, 3];
    const ids = Object.keys(T), end = T[ids[ids.length - 1]];
    S.tempo(bpm, { t0: 0 });
    S.program("pad", "pad", { rev: 0.5, gain: -8 });
    S.program("keys", "ep", { rev: 0.25, bright: 1.2 });
    S.program("bass", "bass");
    S.program("mallet", "marimba", { pan: 0.25, rev: 0.2 });
    const endBeat = Math.floor((end - 0.3) / B);
    for (let b = 0, i = 0; b + 4 <= endBeat; b += 4, i++) {
      const d = prog[i % 4], ch = triad(d, 1);
      if (b < 8) {
        S.chord("pad", ch, { beat: b, beats: 4, vel: 34 });
        S.pattern("mallet", "x.x.x.xx", { from: b, pitch: [ch[2] + 12, ch[1] + 12], vel: 40 });
        continue;
      }
      S.chord("pad", ch, { beat: b, beats: 4, vel: 22 });
      S.chord("keys", ch, { beat: b, beats: 0.5, vel: 48 });
      S.chord("keys", ch, { beat: b + 1.5, beats: 0.5, vel: 42 });
      S.chord("keys", ch, { beat: b + 2.5, beats: 1, vel: 44 });
      S.note("bass", at(d, -1), { beat: b, beats: 0.75, vel: 74 });
      S.note("bass", at(d, -1), { beat: b + 1.5, beats: 0.5, vel: 60 });
      S.note("bass", at(d, 0), { beat: b + 2, beats: 0.5, vel: 56 });
      S.note("bass", at(d, -1), { beat: b + 3, beats: 0.75, vel: 64 });
      S.pattern("mallet", "x.xx.x.x", { from: b, pitch: [ch[2] + 12, ch[0] + 12, ch[1] + 12], vel: 40 });
      if (music.drums !== false) S.drums({ kick: "x...x...", sidestick: "..x...x.", hat: "xoxoxoxo" }, { from: b, vel: 50 });
    }
    // Sound cues follow the cut list and the stock scenes' entrances.
    film.scenes.forEach((s, i) => {
      const t0 = T[s.id];
      if (i > 0 && i < ids.length - 1) S.fx("whoosh", t0 + 0.05, { dur: 0.6, vel: 50 });
      const n = ((s.data && (s.data.items || s.data.chips || s.data.stats)) || []).length;
      for (let j = 0; j < n; j++) S.fx("pop", t0 + 0.45 + j * (s.type === "chips" ? 0.12 : 0.25), { vel: 38 });
      if (s.type === "code" && s.data && s.data.typed) {
        const { TYPE_AT, TYPE_CPS } = kit.stockTiming;
        S.ticks(Array.from({ length: s.data.typed.length }, (_, j) => t0 + TYPE_AT + j / TYPE_CPS), { vel: 24 });
      }
    });
    const fin = triad(prog[0], 1);
    S.run("mallet", [0, 1, 2, 4, 5, 7].map((d) => at(d, 2)), { t: end - 0.6, step: 0.1, vel: 50, cresc: 10 });
    S.chord("keys", [...fin, at(0, 2)], { t: end, dur: 5, vel: 64 });
    S.chord("pad", fin, { t: end, dur: 6, vel: 40 });
    S.note("bass", at(0, -1), { t: end, dur: 4, vel: 80 });
    S.drum("crash", { t: end, vel: 48 });
    S.master({ gain: -6, fadeOut: 1.6 });
  };
})();
