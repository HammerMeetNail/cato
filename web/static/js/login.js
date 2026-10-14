// login.js — sign in / create an account. Only offers what this server
// supports: Google when configured, sign-up while CATO_SIGNUP allows it (or
// with an invite link).
import { api, setCSRF } from './core/api.js';
import { requestOptions, credentialJSON, passkeysPossible } from './core/b64.js';

// Trusted Types (CSP require-trusted-types-for 'script'): registering the
// service worker needs a TrustedScriptURL, from a policy that allows only it.
const swURL = window.trustedTypes
  ? window.trustedTypes.createPolicy('cato-sw', {
    createScriptURL: (u) => { if (u !== '/service-worker.js') throw new TypeError('blocked script URL'); return u; },
  }).createScriptURL('/service-worker.js')
  : '/service-worker.js';

const $ = id => document.getElementById(id);
const form = $('loginForm');
const errorMsg = $('errorMsg');
const submitBtn = $('submitBtn');
const password = $('password');
const params = new URLSearchParams(location.search);
const invite = params.get('invite') || '';
const reset = params.get('reset') || '';
let isSignup = false;

function showError(msg) {
  errorMsg.textContent = msg;
}

function setMode(signup) {
  isSignup = signup;
  $('formTitle').textContent = signup ? 'Create your account' : 'Sign in';
  submitBtn.textContent = signup ? 'Create account' : 'Sign in';
  $('toggleText').textContent = signup ? 'Already have an account?' : 'New here?';
  $('toggleLink').textContent = signup ? 'Sign in' : 'Create an account';
  // Password managers offer to generate a password only for new-password.
  password.autocomplete = signup ? 'new-password' : 'current-password';
  showError('');
}

async function main() {
  try {
    const me = await api.get('/api/me');
    if (me.authenticated) {
      location.href = '/' + location.hash;
      return;
    }
  } catch { /* offline or server down: still show the form */ }

  if (reset) {
    // An admin-made reset link: only a new password is needed.
    $('formTitle').textContent = 'Choose a new password';
    $('pitch').textContent = 'Setting a new password signs you out on your other devices.';
    $('email').closest('label').hidden = true;
    $('email').required = false;
    password.autocomplete = 'new-password';
    submitBtn.textContent = 'Set password';
    $('googleBtn').hidden = $('googleDivider').hidden = $('signupFooter').hidden = true;
    form.addEventListener('submit', async e => {
      e.preventDefault();
      showError('');
      submitBtn.disabled = true;
      try {
        const res = await api.post('/api/auth/reset', { token: reset, password: password.value });
        setCSRF(res.csrf_token);
        location.href = '/';
      } catch (err) {
        showError(err.message || 'Could not reset the password');
        submitBtn.disabled = false;
      }
    });
    return;
  }

  let config = { google_enabled: false, signup_allowed: false };
  try { config = await api.get('/api/config' + (invite ? `?invite=${encodeURIComponent(invite)}` : '')); } catch { /* defaults */ }
  $('googleBtn').hidden = !config.google_enabled;
  $('googleDivider').hidden = !config.google_enabled;
  if (invite && config.invite_valid) {
    $('googleBtn').href = `/api/auth/google/start?invite=${encodeURIComponent(invite)}`;
    $('pitch').textContent = config.invited_by ? `${config.invited_by} invited you to Cato.` : "You've been invited to Cato.";
  }
  const canSignup = config.signup_allowed || (invite && config.invite_valid);
  $('signupFooter').hidden = !canSignup;
  if (invite && config.invite_valid) setMode(true);
  if (invite && !config.invite_valid) showError('This invite link has expired or was already used. Ask for a new one.');
  if (params.get('error')) showError(params.get('error'));

  // Passkeys: offered when the browser supports them and Cato is reached by
  // a host name (an IP address can't hold a passkey).
  if (passkeysPossible() && !(invite && config.invite_valid)) {
    $('passkeyBtn').hidden = false;
    $('googleDivider').hidden = false;
    $('passkeyBtn').addEventListener('click', async () => {
      showError('');
      $('passkeyBtn').disabled = true;
      try {
        const { publicKey } = await api.post('/api/auth/passkey/begin', {});
        const cred = await navigator.credentials.get({ publicKey: requestOptions(publicKey) });
        const res = await api.post('/api/auth/passkey/finish', credentialJSON(cred));
        setCSRF(res.csrf_token);
        location.href = '/' + location.hash;
      } catch (err) {
        if (err?.name !== 'NotAllowedError') showError(err.message || 'Passkey sign-in failed');
        $('passkeyBtn').disabled = false;
      }
    });
  }

  $('toggleLink').addEventListener('click', () => setMode(!isSignup));
  form.addEventListener('submit', async e => {
    e.preventDefault();
    showError('');
    submitBtn.disabled = true;
    const body = { email: $('email').value, password: password.value };
    if (isSignup && invite) body.invite = invite;
    try {
      const res = await api.post(isSignup ? '/api/auth/signup' : '/api/auth/login', body);
      setCSRF(res.csrf_token);
      location.href = '/' + location.hash;
    } catch (err) {
      showError(err.status === 0 ? "Can't reach Cato. Check your connection." : err.message || 'Sign-in failed');
      submitBtn.disabled = false;
    }
  });
}

if ('serviceWorker' in navigator) navigator.serviceWorker.register(swURL).catch(() => {});
main();
