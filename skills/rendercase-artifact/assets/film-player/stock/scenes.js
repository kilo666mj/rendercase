// Stock scene types, so a film can be pure data: each scene in scenes.json names a
// "type" and carries its text in "data". Every entrance is done by about 1.5 s,
// then the scene holds still to be read. Text is always set with textContent.
(function () {
  const { seg, eo, eob, eio, lerp, TextFx } = kit;
  const TYPE_AT = 0.9, TYPE_CPS = 30;
  kit.stockTiming = { TYPE_AT, TYPE_CPS };

  const el = (tag, cls, text) => {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  };
  const rise = (e, p) => {
    e.style.opacity = eo(p);
    e.style.transform = "translateY(" + lerp(26, 0, eo(p)) + "px) scale(" + lerp(0.94, 1, eob(p, 1.5)) + ")";
  };
  // The film's logo.html module, when it has one, marks the title, end and a corner bug.
  const logo = (k, cls) => {
    const src = k.module("logo.html");
    if (!src) return null;
    const box = el("div", cls);
    box.append(k.h(src));
    return box;
  };
  function frame(k, withBug) {
    const g = el("div", "ground"), col = el("div", "st");
    g.append(col);
    if (withBug) { const b = logo(k, "st-bug"); if (b) g.append(b); }
    k.stage.append(g);
    return col;
  }
  function headline(col, d, tag) {
    const words = {};
    if (d.kicker) col.append((words.kicker = el("p", "kicker", d.kicker)));
    if (d.title) col.append((words.line = el(tag || "h2", "line", d.title)));
    return words;
  }
  function tail(col, d) {
    const out = [];
    if (d.sub) out.push(col.appendChild(el("p", "sub", d.sub)));
    if (d.tag) out.push(col.appendChild(el("p", "tag", d.tag)));
    return out;
  }
  // t0 < 0 lets the first scene's headline already show on frame 0, the thumbnail.
  function animateHead(w, t, t0) {
    if (w.kicker) w.kicker.style.opacity = eo(seg(t, t0, t0 + 0.4));
    if (w.line) TextFx.fadeFromRight(w.line, t, t0);
  }
  const fadeAll = (els, t, t0) => els.forEach((e, i) => { e.style.opacity = eo(seg(t, t0 + i * 0.2, t0 + 0.4 + i * 0.2)); });

  kit.type("title", (d) => ({
    init(k) {
      const col = frame(k, false);
      this.logo = logo(k, "st-logo");
      if (this.logo) col.append(this.logo);
      this.w = headline(col, d, "h1");
      this.w.line && (this.w.line.style.fontSize = "150px");
      this.tail = tail(col, { sub: d.subtitle });
      const row = el("div", "st-row");
      this.stats = (d.stats || []).map((s) => {
        const b = el("div", "blk");
        const n = el("span", "nm", s.value);
        n.style.cssText = "font-size:96px;line-height:1;color:var(--accent);font-family:var(--display, inherit)";
        b.append(n, el("span", "ds", s.label));
        row.append(b);
        return b;
      });
      if (this.stats.length) col.append(row);
    },
    render(t) {
      if (this.logo) this.logo.style.opacity = eo(seg(t, -0.6, -0.2));
      animateHead(this.w, t, -0.6);
      fadeAll(this.tail, t, 0.3);
      this.stats.forEach((b, i) => rise(b, seg(t, 0.5 + i * 0.3, 1.0 + i * 0.3)));
    },
  }));

  kit.type("statement", (d) => ({
    init(k) {
      const col = frame(k, true);
      this.w = headline(col, d);
      this.tail = tail(col, d);
    },
    render(t) { animateHead(this.w, t, 0); fadeAll(this.tail, t, 0.6); },
  }));

  kit.type("stat", (d) => ({
    init(k) {
      const col = frame(k, true);
      this.w = headline(col, { kicker: d.kicker });
      this.big = col.appendChild(el("div", "big", d.value));
      // Numbers count up; anything else simply fades in.
      const m = /^([^\d]*)([\d,]+(?:\.\d+)?)(.*)$/.exec(d.value);
      if (m) {
        const raw = m[2].replace(/,/g, ""), places = (raw.split(".")[1] || "").length;
        this.count = (p) => m[1] + (+raw * p).toLocaleString("en-US", { minimumFractionDigits: places, maximumFractionDigits: places, useGrouping: m[2].includes(",") }) + m[3];
      }
      this.label = col.appendChild(el("h2", "line", d.label));
      this.label.style.fontSize = "72px";
      this.tail = tail(col, { sub: d.detail, tag: d.tag });
    },
    render(t) {
      animateHead(this.w, t, 0);
      const p = seg(t, 0, 1.2);
      this.big.style.opacity = eo(seg(t, 0, 0.3));
      if (this.count) this.big.textContent = this.count(eio(p));
      TextFx.fadeFromRight(this.label, t, 0.3);
      fadeAll(this.tail, t, 0.9);
    },
  }));

  kit.type("list", (d) => ({
    init(k) {
      const col = frame(k, true);
      this.w = headline(col, d);
      this.items = (d.items || []).map((it) => {
        it = typeof it === "string" ? { name: it } : it;
        const b = el("div", "blk" + (it.hot ? " hot" : ""));
        b.append(el("span", "nm", it.name));
        if (it.desc) b.append(el("span", "ds", it.desc));
        col.append(b);
        return b;
      });
      this.tail = tail(col, { tag: d.tag });
    },
    render(t) {
      animateHead(this.w, t, 0);
      this.items.forEach((b, i) => rise(b, seg(t, 0.4 + i * 0.22, 0.85 + i * 0.22)));
      fadeAll(this.tail, t, 1.2);
    },
  }));

  kit.type("chips", (d) => ({
    init(k) {
      const col = frame(k, true);
      this.w = headline(col, d);
      const row = col.appendChild(el("div", "st-chips"));
      this.chips = (d.chips || []).map((c, i) => row.appendChild(el("span", "chip" + (i === d.hot ? " hot" : ""), c)));
      this.tail = tail(col, d);
    },
    render(t) {
      animateHead(this.w, t, 0);
      this.chips.forEach((c, i) => {
        const p = seg(t, 0.4 + i * 0.12, 0.75 + i * 0.12);
        c.style.opacity = eo(p);
        c.style.transform = "translateY(" + lerp(24, 0, eo(p)) + "px) scale(" + lerp(0.9, 1, eob(p, 1.7)) + ")";
      });
      fadeAll(this.tail, t, 1.1);
    },
  }));

  kit.type("code", (d) => ({
    init(k) {
      const col = frame(k, true);
      this.w = headline(col, d);
      const row = col.appendChild(el("div", "st-row"));
      const left = row.appendChild(el("div"));
      left.style.cssText = "display:flex;flex-direction:column;gap:24px;justify-content:center";
      this.tail = [d.sub && left.appendChild(el("p", "sub", d.sub)), d.tag && left.appendChild(el("p", "tag", d.tag))].filter(Boolean);
      if (!this.tail.length) row.removeChild(left);
      this.card = row.appendChild(el("div", "card"));
      if (d.file) this.card.append(el("div", "hd", d.file));
      const pre = this.card.appendChild(el("pre", "code"));
      pre.append(d.before || "");
      this.val = pre.appendChild(el("span", "hl"));
      pre.append(d.after || "");
      this.typed = kit.typist(d.typed || "", TYPE_AT, TYPE_CPS);
    },
    render(t) {
      animateHead(this.w, t, 0);
      rise(this.card, seg(t, 0.3, 0.75));
      const s = this.typed.at(t);
      this.val.textContent = s;
      this.val.style.opacity = s ? 1 : 0;
      fadeAll(this.tail, t, 0.6);
    },
  }));

  kit.type("quote", (d) => ({
    init(k) {
      const col = frame(k, true);
      this.q = col.appendChild(el("blockquote", "st-quote", d.text));
      this.q.style.margin = "0";
      this.tail = tail(col, { sub: d.attribution && "— " + d.attribution });
    },
    render(t) { TextFx.fadeFromRight(this.q, t, 0); fadeAll(this.tail, t, 1.2); },
  }));

  kit.type("end", (d) => ({
    init(k) {
      const col = frame(k, false);
      this.logo = logo(k, "st-logo");
      if (this.logo) col.append(this.logo);
      this.w = headline(col, d);
      this.w.line && (this.w.line.style.fontSize = "130px");
      this.tail = tail(col, d);
      this.bar = col.appendChild(el("div", "rule"));
      this.bar.style.width = "560px";
    },
    render(t) {
      if (this.logo) {
        const p = seg(t, 0, 0.6);
        this.logo.style.opacity = eo(p);
        this.logo.style.transformOrigin = "0 50%";
        this.logo.style.transform = "scale(" + lerp(0.92, 1, eob(p, 1.5)) + ")";
      }
      animateHead(this.w, t, 0.4);
      fadeAll(this.tail, t, 0.9);
      this.bar.style.transform = "scaleX(" + eio(seg(t, 1.0, 1.5)) + ")";
    },
  }));
})();
