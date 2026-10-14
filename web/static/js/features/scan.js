// scan.js — add a physical game by its barcode (plan §8.3). The camera
// reads UPC/EAN codes where the browser has BarcodeDetector (Android,
// desktop Chrome); elsewhere (iPhone) you type the number under the
// barcode. Cato looks the code up once (UPCitemdb), matches the product
// title to the catalog, and remembers the game you confirm.
import { html, nothing, ref } from '../ui/lit.js';
import { icon } from '../ui/icons.js';
import { cover } from '../ui/parts.js';
import { openSheet } from '../ui/sheet.js';
import { api } from '../core/api.js';
import { go } from '../core/router.js';

const canDetect = () => typeof globalThis.BarcodeDetector === 'function';

export function openScanner(onSearch) {
  const st = { code: '', busy: false, result: null, error: '', camera: canDetect() && !!navigator.mediaDevices?.getUserMedia };
  let stream = null;
  let timer = null;
  const stop = () => { clearInterval(timer); stream?.getTracks().forEach(t => t.stop()); stream = null; };
  const lookup = async (s, code) => {
    st.code = code;
    st.busy = true; st.error = ''; st.result = null; s.update();
    try {
      st.result = await api.get(`/api/v1/barcode/${encodeURIComponent(code)}`);
      if (!st.result.confirmed && !st.result.candidate?.match && !st.result.title) st.error = 'Nothing is known about that barcode. Search by name instead.';
    } catch (err) {
      st.error = err.message;
    }
    st.busy = false; s.update();
  };
  const startCamera = async (video, s) => {
    if (!video || stream) return;
    try {
      stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' }, audio: false });
      video.srcObject = stream;
      await video.play();
      const detector = new BarcodeDetector({ formats: ['ean_13', 'ean_8', 'upc_a', 'upc_e'] });
      timer = setInterval(async () => {
        if (st.busy || st.result) return;
        try {
          const [hit] = await detector.detect(video);
          if (hit?.rawValue) { stop(); navigator.vibrate?.(30); lookup(s, hit.rawValue); }
        } catch { /* frame not ready */ }
      }, 300);
    } catch {
      st.camera = false;
      st.error = 'The camera isn’t available. Type the number under the barcode instead.';
      s.update();
    }
  };
  const pick = async (s, game) => {
    try { await api.put(`/api/v1/barcode/${encodeURIComponent(st.code)}`, { game_id: game.id }); } catch { /* still open it */ }
    s.close();
    go(`#game/${game.id}`);
  };
  openSheet({
    title: 'Scan a barcode',
    onClose: stop,
    content: s => {
      const r = st.result;
      const game = r?.confirmed || r?.candidate?.match;
      return html`<div class="scanner form-grid">
        ${st.camera && !r ? html`<video playsinline muted aria-label="Camera" ${ref(v => startCamera(v, s))}></video>
          <p class="muted">Point the camera at the barcode on the box.</p>` : nothing}
        ${!canDetect() ? html`<p class="muted">${icon('info')} This browser can’t read barcodes from the camera. Type the number under the barcode.</p>` : nothing}
        <form class="row" @submit=${e => { e.preventDefault(); const v = new FormData(e.target).get('code').replace(/\D/g, ''); if (v) lookup(s, v); }}>
          <input class="input grow" name="code" inputmode="numeric" pattern="[0-9 ]{8,16}" placeholder="UPC or EAN number" aria-label="Barcode number" .value=${st.code}>
          <button class="btn btn-secondary" type="submit" ?disabled=${st.busy}>Look up</button></form>
        ${st.busy ? html`<p class="muted" role="status">Looking it up…</p>` : nothing}
        ${st.error ? html`<p class="field-error" role="alert">${st.error}</p>` : nothing}
        ${game ? html`<div class="card"><p class="muted">${r.confirmed ? 'This barcode is' : `“${r.title}” looks like`}</p>
          <button type="button" class="list-row" @click=${() => pick(s, game)}>${cover(game, { cls: 'thumb' })}
            <div class="grow"><div class="title">${game.name}</div><div class="sub">${game.year || ''}</div></div>${icon('chevron-right')}</button></div>` : nothing}
        ${r?.title ? html`<button type="button" class="link" @click=${() => { s.close(); onSearch?.(r.candidate?.row?.title || r.title); }}>Not it? Search for “${r.candidate?.row?.title || r.title}”</button>` : nothing}
      </div>`;
    },
  });
}
