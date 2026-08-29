import { checkAuth, updateMe, changePassword, deleteAccount, library } from './api.js';

function escapeHTML(str) {
  return String(str ?? '').replace(/[&<>"']/g, c => ({
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;',
  }[c]));
}

function buildSettingsHTML(auth) {
  const email = escapeHTML(auth.email || '—');
  const displayName = escapeHTML(auth.display_name || '');
  const hasPassword = auth.has_password !== false;

  const securityHTML = hasPassword ? `
        <form id="changePasswordForm">
          <div class="settings-row settings-edit-row">
            <label for="currentPasswordInput" class="settings-label">Current password</label>
            <input type="password" id="currentPasswordInput" class="settings-input" autocomplete="current-password" required>
          </div>
          <div class="settings-row settings-edit-row">
            <label for="newPasswordInput" class="settings-label">New password</label>
            <input type="password" id="newPasswordInput" class="settings-input" autocomplete="new-password" minlength="8" required>
          </div>
          <div class="settings-row settings-edit-row">
            <label for="confirmPasswordInput" class="settings-label">Confirm new password</label>
            <input type="password" id="confirmPasswordInput" class="settings-input" autocomplete="new-password" minlength="8" required>
          </div>
          <div class="settings-feedback" id="passwordFeedback"></div>
          <button type="submit" class="btn btn-secondary settings-btn">Update password</button>
        </form>` : `<div class="settings-row">This account signs in with Google.</div>`;

  return `
    <div class="settings-page">
      <h2>Settings</h2>

      <section class="settings-section">
        <h3>Account</h3>
        <div class="settings-row"><span class="settings-label">Email</span><span id="accountEmail">${email}</span></div>
        <div class="settings-row settings-edit-row">
          <label for="displayNameInput" class="settings-label">Display name</label>
          <span class="settings-edit">
            <input type="text" id="displayNameInput" maxlength="64" autocomplete="nickname" value="${displayName}">
            <button type="button" class="btn btn-secondary settings-save-btn" id="saveNameBtn">Save</button>
          </span>
        </div>
        <div class="settings-feedback" id="nameFeedback"></div>
      </section>

      <section class="settings-section">
        <h3>Appearance</h3>
        <div class="settings-row settings-edit-row">
          <label for="themeSelect" class="settings-label">Theme</label>
          <select id="themeSelect" class="theme-select">
            <option value="">Dark (default)</option>
            <option value="light">Light</option>
          </select>
        </div>
      </section>

      <section class="settings-section" id="passwordSection">
        <h3>Security</h3>
        ${securityHTML}
      </section>

      <section class="settings-section">
        <h3>Library</h3>
        <div class="settings-row" id="librarySummary">Counting games…</div>
        <a href="/api/library/export" class="btn btn-secondary settings-btn" download>Export library as CSV</a>
      </section>

      <section class="settings-section settings-danger">
        <h3>Danger zone</h3>
        <p class="settings-danger-text">Deleting your account permanently removes your profile,
          entire library and playtime history. This cannot be undone.</p>
        <button type="button" class="btn btn-danger settings-btn" id="deleteAccountBtn">Delete account…</button>
        <div id="deleteAccountConfirm" hidden>
          <div class="settings-row settings-edit-row">
            <label for="deleteConfirmInput" class="settings-label">Type DELETE to confirm</label>
            <input type="text" id="deleteConfirmInput" class="settings-input" autocomplete="off"
                   autocapitalize="characters" spellcheck="false" placeholder="DELETE">
          </div>
          <div class="settings-feedback settings-feedback-error" id="deleteFeedback"></div>
          <button type="button" class="btn btn-danger settings-btn" id="confirmDeleteBtn" disabled>Permanently delete my account</button>
          <button type="button" class="btn btn-secondary settings-btn" id="cancelDeleteBtn">Cancel</button>
        </div>
      </section>
    </div>`;
}

function wireSettings(container, auth) {
  // Account: display name save
  const nameInput = container.querySelector('#displayNameInput');
  const feedback = container.querySelector('#nameFeedback');
  const saveNameBtn = container.querySelector('#saveNameBtn');
  if (saveNameBtn && nameInput && feedback) {
    saveNameBtn.addEventListener('click', async () => {
      feedback.textContent = '';
      try {
        const updated = await updateMe({ display_name: nameInput.value });
        nameInput.value = updated.display_name || '';
        feedback.textContent = 'Saved';
        // Also refresh topbar display name if present
        const topDisplay = document.getElementById('userDisplay');
        if (topDisplay) {
          if (updated.display_name) {
            topDisplay.textContent = updated.display_name;
            topDisplay.style.display = '';
          } else {
            topDisplay.style.display = 'none';
          }
        }
        setTimeout(() => { feedback.textContent = ''; }, 2000);
      } catch (err) {
        feedback.textContent = err.message || 'Failed to save';
      }
    });
  }

  // Theme
  const themeSelect = container.querySelector('#themeSelect');
  if (themeSelect) {
    themeSelect.value = localStorage.getItem('cato-theme') || '';
    themeSelect.addEventListener('change', () => {
      const v = themeSelect.value;
      if (v) {
        localStorage.setItem('cato-theme', v);
        document.documentElement.setAttribute('data-theme', v);
      } else {
        localStorage.removeItem('cato-theme');
        document.documentElement.removeAttribute('data-theme');
      }
    });
  }

  // Password: only if has_password !== false
  if (auth.has_password !== false) {
    const pwForm = container.querySelector('#changePasswordForm');
    const currentPw = container.querySelector('#currentPasswordInput');
    const newPw = container.querySelector('#newPasswordInput');
    const confirmPw = container.querySelector('#confirmPasswordInput');
    const pwFeedback = container.querySelector('#passwordFeedback');
    if (pwForm && currentPw && newPw && confirmPw && pwFeedback) {
      pwForm.addEventListener('submit', async (e) => {
        e.preventDefault();
        pwFeedback.classList.remove('settings-feedback-error');
        if (newPw.value !== confirmPw.value) {
          pwFeedback.classList.add('settings-feedback-error');
          pwFeedback.textContent = 'New passwords do not match';
          return;
        }
        try {
          await changePassword(currentPw.value, newPw.value);
          pwForm.reset();
          pwFeedback.textContent = 'Password updated';
          setTimeout(() => { pwFeedback.textContent = ''; }, 3000);
        } catch (err) {
          pwFeedback.classList.add('settings-feedback-error');
          pwFeedback.textContent = err.message || 'Failed to change password';
        }
      });
    }
  }

  // Delete account: two-step typed confirmation.
  const deleteBtn = container.querySelector('#deleteAccountBtn');
  const deleteConfirmWrap = container.querySelector('#deleteAccountConfirm');
  const deleteInput = container.querySelector('#deleteConfirmInput');
  const confirmDeleteBtn = container.querySelector('#confirmDeleteBtn');
  const cancelDeleteBtn = container.querySelector('#cancelDeleteBtn');
  const deleteFeedback = container.querySelector('#deleteFeedback');
  if (deleteBtn && deleteConfirmWrap && deleteInput && confirmDeleteBtn && cancelDeleteBtn) {
    deleteBtn.addEventListener('click', () => {
      deleteBtn.hidden = true;
      deleteConfirmWrap.hidden = false;
      deleteInput.focus();
    });
    cancelDeleteBtn.addEventListener('click', () => {
      deleteConfirmWrap.hidden = true;
      deleteBtn.hidden = false;
      deleteInput.value = '';
      confirmDeleteBtn.disabled = true;
      if (deleteFeedback) deleteFeedback.textContent = '';
    });
    deleteInput.addEventListener('input', () => {
      confirmDeleteBtn.disabled = deleteInput.value.trim() !== 'DELETE';
    });
    confirmDeleteBtn.addEventListener('click', async () => {
      if (deleteFeedback) deleteFeedback.textContent = '';
      try {
        await deleteAccount();
        window.location.href = '/login';
      } catch (err) {
        if (deleteFeedback) deleteFeedback.textContent = err.message || 'Failed to delete account';
      }
    });
  }

  // Library summary
  const summaryEl = container.querySelector('#librarySummary');
  if (summaryEl) {
    library.counts().then(counts => {
      if (counts && counts.all >= 0) {
        const hours = counts.total_minutes > 0 ? Math.round(counts.total_minutes / 60) : 0;
        summaryEl.textContent =
          `${counts.all} game${counts.all === 1 ? '' : 's'} · ${counts.completed_count || 0} finished` +
          (hours > 0 ? ` · ~${hours}h logged` : '');
      } else {
        summaryEl.textContent = '—';
      }
    }).catch(() => {
      summaryEl.textContent = '—';
    });
  }
}

export async function renderSettingsView(container) {
  if (!container) return;
  // Loading placeholder
  container.innerHTML = '<div class="settings-page"><div class="loading">Loading settings…</div></div>';
  let auth;
  try {
    auth = await checkAuth();
  } catch {
    auth = { authenticated: false };
  }
  if (!auth.authenticated) {
    window.location.href = '/login';
    return;
  }
  container.innerHTML = buildSettingsHTML(auth);
  wireSettings(container, auth);
}

// For compatibility with plan naming: initSettings is alias
export const initSettings = renderSettingsView;
