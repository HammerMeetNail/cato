// login.js — login/signup page controller (was an inline module in
// login.html; externalized so the CSP can use script-src 'self').
import { checkAuth, login, signup } from './api.js';

const form = document.getElementById('loginForm');
const errorMsg = document.getElementById('errorMsg');
const submitBtn = document.getElementById('submitBtn');
const toggleLink = document.getElementById('toggleLink');
const toggleText = document.getElementById('toggleText');
const googleBtn = document.getElementById('googleBtn');

let isSignup = false;

// Redirect if already logged in
const auth = await checkAuth();
if (auth.authenticated) {
  window.location.href = '/' + window.location.hash;
  throw new Error('Already authenticated');
}

toggleLink.addEventListener('click', () => {
  isSignup = !isSignup;
  submitBtn.textContent = isSignup ? 'Sign Up' : 'Login';
  toggleText.textContent = isSignup ? 'Already have an account?' : "Don't have an account?";
  toggleLink.textContent = isSignup ? 'Login' : 'Sign up';
  hideError();
});

form.addEventListener('submit', async (e) => {
  e.preventDefault();
  hideError();

  const email = document.getElementById('email').value;
  const password = document.getElementById('password').value;

  try {
    if (isSignup) {
      await signup(email, password);
    } else {
      await login(email, password);
    }
    window.location.href = '/' + window.location.hash;
  } catch (err) {
    showError(err.message || 'Authentication failed');
  }
});

googleBtn.addEventListener('click', () => {
  window.location.href = '/api/auth/google/start';
});

function showError(msg) {
  errorMsg.textContent = msg;
  errorMsg.classList.add('show');
}

function hideError() {
  errorMsg.textContent = '';
  errorMsg.classList.remove('show');
}
if ('serviceWorker' in navigator) {
  navigator.serviceWorker.register('/service-worker.js').catch(() => {});
}
