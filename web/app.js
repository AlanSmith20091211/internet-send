(() => {
  'use strict';

  const $ = (sel) => document.querySelector(sel);
  const enc = encodeURIComponent;

  // localStorage can throw in private mode; the app still works without it.
  const store = {
    get(k) { try { return localStorage.getItem(k); } catch { return null; } },
    set(k, v) { try { localStorage.setItem(k, v); } catch { /* ignore */ } },
  };

  // crypto.randomUUID needs a secure context (HTTPS), which a LAN address is not.
  function randomID() {
    const b = new Uint8Array(12);
    crypto.getRandomValues(b);
    return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
  }

  function detectPlatform() {
    const ua = navigator.userAgent;
    if (/iPhone/.test(ua)) return 'iPhone';
    if (/iPad/.test(ua) || (/Macintosh/.test(ua) && navigator.maxTouchPoints > 1)) return 'iPad';
    if (/HarmonyOS/i.test(ua)) return '鸿蒙';
    if (/Android/.test(ua)) return 'Android';
    if (/Windows/.test(ua)) return 'Windows';
    if (/Mac OS X/.test(ua)) return 'Mac';
    if (/Linux/.test(ua)) return 'Linux';
    return '';
  }

  const ADJ = ['机灵的', '安静的', '勇敢的', '快乐的', '闪亮的', '温柔的', '敏捷的', '好奇的', '淡定的', '活泼的'];
  const ANIMAL = ['熊猫', '狐狸', '海豚', '猫头鹰', '企鹅', '松鼠', '鲸鱼', '兔子', '老虎', '考拉', '柴犬', '水獭'];
  const pick = (a) => a[Math.floor(Math.random() * a.length)];

  const platform = detectPlatform();
  const me = {
    id: store.get('isend.id') || randomID(),
    name: store.get('isend.name') || `${pick(ADJ)}${pick(ANIMAL)}`,
  };
  store.set('isend.id', me.id);
  store.set('isend.name', me.name);

  const state = {
    devices: [],
    items: new Map(),
    target: '', // '' = all devices
    es: null,
    info: null,
    ready: false,
  };

  // ---------- formatting ----------

  function fmtSize(n) {
    if (n < 1024) return `${n} B`;
    const units = ['KB', 'MB', 'GB', 'TB'];
    let i = -1;
    do { n /= 1024; i++; } while (n >= 1024 && i < units.length - 1);
    return `${n.toFixed(n >= 100 ? 0 : 1)} ${units[i]}`;
  }

  function fmtTime(t) {
    const d = new Date(t);
    const pad = (x) => String(x).padStart(2, '0');
    const today = new Date().toDateString() === d.toDateString();
    return (today ? '' : `${d.getMonth() + 1}/${d.getDate()} `) + `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }

  function extLabel(name) {
    const m = /\.([a-z0-9]{1,5})$/i.exec(name || '');
    return m ? m[1].toUpperCase() : 'FILE';
  }

  function el(tag, attrs = {}, ...children) {
    const node = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) {
      if (v == null || v === false) continue;
      if (k === 'class') node.className = v;
      else if (k.startsWith('on')) node.addEventListener(k.slice(2), v);
      else node.setAttribute(k, v === true ? '' : v);
    }
    for (const c of children) if (c != null) node.append(c);
    return node;
  }

  function toast(msg, isErr = false) {
    const t = el('div', { class: 'toast' + (isErr ? ' err' : '') }, msg);
    $('#toasts').append(t);
    setTimeout(() => t.remove(), isErr ? 5000 : 3000);
  }

  // ---------- API ----------

  async function api(path, opts = {}) {
    const res = await fetch(path, { credentials: 'same-origin', ...opts });
    let data = null;
    try { data = await res.json(); } catch { /* not JSON */ }
    if (!res.ok) {
      const err = new Error((data && data.error) || `请求失败（${res.status}）`);
      err.status = res.status;
      throw err;
    }
    return data;
  }

  // ---------- connection ----------

  function setStatus(s, text) {
    $('#status').dataset.state = s;
    $('#statusText').textContent = text;
  }

  async function start() {
    try {
      state.info = await api('api/info');
    } catch {
      setStatus('offline', '无法连接');
      setTimeout(start, 3000);
      return;
    }
    $('#foot').textContent = `Internet Send ${state.info.version} · 主机 ${state.info.name || ''}`;
    if (!state.info.authorized) {
      askPIN();
      return;
    }
    connect();
  }

  function connect() {
    if (state.es) state.es.close();
    setStatus('connecting', '连接中…');
    const es = new EventSource(`api/events?device=${enc(me.id)}&name=${enc(me.name)}&platform=${enc(platform)}`);
    state.es = es;

    es.addEventListener('hello', (e) => {
      const h = JSON.parse(e.data);
      state.items.clear();
      for (const it of h.items) state.items.set(it.id, it);
      state.devices = h.devices;
      state.ready = true;
      setStatus('online', '已连接');
      renderDevices();
      renderItems();
    });
    es.addEventListener('devices', (e) => {
      state.devices = JSON.parse(e.data);
      renderDevices();
    });
    es.addEventListener('item', (e) => {
      const it = JSON.parse(e.data);
      if (state.items.has(it.id)) return;
      state.items.set(it.id, it);
      addItem(it, it.from !== me.id);
      if (it.from !== me.id) {
        toast(`${it.fromName} 发来${it.kind === 'file' ? `文件「${it.name}」` : '一段文字'}`);
        notifyTitle();
      }
    });
    es.onerror = async () => {
      setStatus('offline', '重新连接中…');
      // EventSource can't see HTTP status; check whether we lost auth.
      try {
        const info = await api('api/info');
        if (!info.authorized) { es.close(); askPIN(); }
      } catch { /* hub unreachable — EventSource keeps retrying */ }
    };
  }

  let titleTimer = null;
  function notifyTitle() {
    if (!document.hidden || titleTimer) return;
    const base = document.title;
    let on = false;
    titleTimer = setInterval(() => { on = !on; document.title = on ? '【新消息】' + base : base; }, 1000);
    const stop = () => {
      if (document.hidden) return;
      clearInterval(titleTimer); titleTimer = null; document.title = base;
      document.removeEventListener('visibilitychange', stop);
    };
    document.addEventListener('visibilitychange', stop);
  }

  // ---------- PIN ----------

  function askPIN() {
    const dlg = $('#pinDialog');
    if (!dlg.open) dlg.showModal();
    $('#pinInput').focus();
  }

  $('#pinForm').addEventListener('submit', async (e) => {
    e.preventDefault();
    $('#pinError').textContent = '';
    try {
      await api('api/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ pin: $('#pinInput').value.trim() }),
      });
      $('#pinDialog').close();
      start();
    } catch (err) {
      $('#pinError').textContent = err.message;
    }
  });
  $('#pinDialog').addEventListener('cancel', (e) => e.preventDefault());

  // ---------- rendering ----------

  function deviceName(id) {
    const d = state.devices.find((x) => x.id === id);
    return d ? d.name : null;
  }

  function renderDevices() {
    const others = state.devices.filter((d) => d.id !== me.id);
    if (state.target && !others.some((d) => d.id === state.target)) state.target = '';

    const chip = (id, label, plat) => el('button', {
      class: 'chip', type: 'button', role: 'radio',
      'aria-checked': String(state.target === id),
      onclick: () => { state.target = id; renderDevices(); },
    }, label, plat ? el('span', { class: 'plat' }, plat) : null);

    const box = $('#targets');
    box.replaceChildren(
      chip('', `所有设备（${others.length}）`),
      ...others.map((d) => chip(d.id, d.name, d.platform)),
    );
    $('#alone').hidden = others.length > 0;
  }

  function fileURL(it, inline) {
    return `api/items/${enc(it.id)}/file?device=${enc(me.id)}${inline ? '&inline=1' : ''}`;
  }

  function renderItem(it, fresh) {
    const mine = it.from === me.id;
    const fromLabel = mine ? '我' : it.fromName;
    const toLabel = it.to ? (it.to === me.id ? '我' : (deviceName(it.to) || it.toName)) : '所有设备';
    const sub = el('div', { class: 'sub' },
      [it.kind === 'file' ? fmtSize(it.size) : null, `${fromLabel} → ${toLabel}`, fmtTime(it.time)]
        .filter(Boolean).join(' · '));

    let icon, main, actions;
    if (it.kind === 'file') {
      const isImage = /^image\/(png|jpe?g|gif|webp|bmp|avif)$/.test(it.mime || '');
      icon = el('div', { class: 'icon' },
        isImage && it.size < 30 * 1024 * 1024
          ? el('img', { src: fileURL(it, true), alt: '', loading: 'lazy' })
          : extLabel(it.name));
      main = el('div', { class: 'title' }, it.name);
      const previewable = /^(image|video|audio)\//.test(it.mime || '') && it.mime !== 'image/svg+xml';
      actions = el('div', { class: 'actions' },
        previewable ? el('a', { class: 'btn small', href: fileURL(it, true), target: '_blank', rel: 'noopener' }, '预览') : null,
        el('a', { class: 'btn small primary', href: fileURL(it, false), download: it.name }, '下载'));
    } else {
      icon = el('div', { class: 'icon' }, 'Aa');
      main = el('pre', { class: 'text' }, it.text);
      const url = /^https?:\/\/\S+$/i.test(it.text.trim()) ? it.text.trim() : null;
      actions = el('div', { class: 'actions' },
        url ? el('a', { class: 'btn small', href: url, target: '_blank', rel: 'noopener noreferrer' }, '打开') : null,
        el('button', { class: 'btn small primary', type: 'button', onclick: () => copyText(it.text) }, '复制'));
    }
    return el('li', { class: 'item' + (fresh ? ' fresh' : ''), 'data-id': it.id },
      icon, el('div', { class: 'body' }, main, sub), actions);
  }

  function renderItems() {
    const list = [...state.items.values()].sort((a, b) => new Date(b.time) - new Date(a.time));
    $('#items').replaceChildren(...list.map((it) => renderItem(it, false)));
  }

  function addItem(it, fresh) {
    const node = renderItem(it, fresh);
    $('#items').prepend(node);
    if (fresh) setTimeout(() => node.classList.remove('fresh'), 4000);
  }

  // navigator.clipboard is unavailable over plain-HTTP LAN addresses, so fall
  // back to the legacy execCommand path.
  async function copyText(text) {
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(text);
      } else {
        const ta = el('textarea', { readonly: true, style: 'position:fixed;opacity:0;top:0' });
        ta.value = text;
        document.body.append(ta);
        ta.select();
        ta.setSelectionRange(0, text.length);
        const ok = document.execCommand('copy');
        ta.remove();
        if (!ok) throw new Error();
      }
      toast('已复制');
    } catch {
      toast('复制失败，请长按文字手动复制', true);
    }
  }

  // ---------- sending ----------

  const queue = [];
  let uploading = false;

  function enqueue(files) {
    if (!state.ready) { toast('尚未连接到主机', true); return; }
    for (const file of files) {
      const job = { file, to: state.target, xhr: null, canceled: false };
      job.view = renderTransfer(job);
      queue.push(job);
    }
    pump();
  }

  function renderTransfer(job) {
    const bar = el('i');
    const meta = el('span', { class: 'meta' }, '等待中');
    const cancel = el('button', {
      class: 'btn small', type: 'button',
      onclick: () => {
        job.canceled = true;
        if (job.xhr) job.xhr.abort();
        node.remove();
      },
    }, '取消');
    const node = el('div', { class: 'transfer' },
      el('div', { class: 'row' }, el('span', { class: 'name' }, job.file.name || '粘贴的图片'), meta, cancel),
      el('div', { class: 'bar' }, bar));
    $('#transfers').append(node);
    return { node, bar, meta, cancel };
  }

  async function pump() {
    if (uploading) return;
    uploading = true;
    while (queue.length) {
      const job = queue.shift();
      if (job.canceled) continue;
      try {
        await upload(job);
        job.view.meta.textContent = '完成';
        job.view.bar.style.width = '100%';
        job.view.cancel.remove();
        setTimeout(() => job.view.node.remove(), 1500);
      } catch (err) {
        if (job.canceled) continue;
        job.view.node.classList.add('failed');
        job.view.meta.textContent = '失败';
        job.view.cancel.textContent = '关闭';
        toast(`「${job.file.name}」发送失败：${err.message}`, true);
      }
    }
    uploading = false;
  }

  function upload(job) {
    return new Promise((resolve, reject) => {
      const { file } = job;
      const name = file.name || `截图-${Date.now()}.png`;
      const xhr = new XMLHttpRequest();
      job.xhr = xhr;
      xhr.open('POST', `api/upload?device=${enc(me.id)}&to=${enc(job.to)}&name=${enc(name)}`);
      xhr.setRequestHeader('Content-Type', 'application/octet-stream');

      const started = performance.now();
      xhr.upload.onprogress = (e) => {
        if (!e.lengthComputable) return;
        const pct = (e.loaded / e.total) * 100;
        const secs = (performance.now() - started) / 1000;
        const speed = secs > 0.3 ? ` · ${fmtSize(e.loaded / secs)}/s` : '';
        job.view.bar.style.width = `${pct}%`;
        job.view.meta.textContent = `${pct.toFixed(0)}% · ${fmtSize(e.loaded)}/${fmtSize(e.total)}${speed}`;
      };
      xhr.onload = () => {
        if (xhr.status >= 200 && xhr.status < 300) return resolve();
        let msg = `HTTP ${xhr.status}`;
        try { msg = JSON.parse(xhr.responseText).error || msg; } catch { /* keep */ }
        reject(new Error(msg));
      };
      xhr.onerror = () => reject(new Error('网络错误'));
      xhr.onabort = () => reject(new Error('已取消'));
      xhr.send(file);
    });
  }

  async function sendText() {
    const ta = $('#textInput');
    const text = ta.value;
    if (!text.trim()) return;
    if (!state.ready) { toast('尚未连接到主机', true); return; }
    try {
      await api('api/text', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ device: me.id, to: state.target, text }),
      });
      ta.value = '';
    } catch (err) {
      toast(`发送失败：${err.message}`, true);
    }
  }

  // ---------- wiring ----------

  $('#fileInput').addEventListener('change', (e) => {
    enqueue([...e.target.files]);
    e.target.value = '';
  });

  const drop = $('#drop');
  ['dragenter', 'dragover'].forEach((t) => drop.addEventListener(t, (e) => { e.preventDefault(); drop.classList.add('over'); }));
  ['dragleave', 'drop'].forEach((t) => drop.addEventListener(t, () => drop.classList.remove('over')));
  drop.addEventListener('drop', (e) => {
    e.preventDefault();
    const files = [...(e.dataTransfer.files || [])];
    if (files.length) enqueue(files);
  });
  // Dropping outside the zone shouldn't navigate away from the page.
  window.addEventListener('dragover', (e) => e.preventDefault());
  window.addEventListener('drop', (e) => e.preventDefault());

  document.addEventListener('paste', (e) => {
    if (e.target === $('#textInput')) {
      const files = [...(e.clipboardData.files || [])];
      if (!files.length) return; // plain text paste into the textarea
      e.preventDefault();
      enqueue(files);
      return;
    }
    const files = [...(e.clipboardData.files || [])];
    if (files.length) { e.preventDefault(); enqueue(files); }
  });

  $('#textForm').addEventListener('submit', (e) => { e.preventDefault(); sendText(); });
  $('#textInput').addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); sendText(); }
  });

  const nameBtn = $('#myName');
  nameBtn.textContent = me.name;
  nameBtn.addEventListener('click', () => {
    const v = prompt('设备名称', me.name);
    if (v == null) return;
    const name = v.trim().slice(0, 40);
    if (!name || name === me.name) return;
    me.name = name;
    store.set('isend.name', name);
    nameBtn.textContent = name;
    if (state.info && state.info.authorized) connect(); // reconnect announces the new name
  });

  // QR code for opening this hub on a phone.
  $('#qrBtn').addEventListener('click', async () => {
    try { state.info = await api('api/info'); } catch { /* use cached */ }
    const urls = [...((state.info && state.info.urls) || [])];
    const here = location.origin;
    if (!/^https?:\/\/(localhost|127\.|\[::1\])/.test(here) && !urls.includes(here)) urls.unshift(here);
    const sel = $('#qrUrl');
    if (!urls.length) {
      toast('没有检测到局域网地址，请确认已连接 Wi-Fi 或热点', true);
      return;
    }
    sel.replaceChildren(...urls.map((u) => el('option', { value: u }, u)));
    sel.hidden = urls.length < 2;
    const show = () => {
      let link = sel.value;
      if (state.info && state.info.pin) link += `/?pin=${enc(state.info.pin)}`;
      $('#qrImg').src = `api/qr?text=${enc(link)}`;
      $('#qrText').textContent = sel.value;
    };
    sel.onchange = show;
    show();
    $('#qrDialog').showModal();
  });

  start();
})();
