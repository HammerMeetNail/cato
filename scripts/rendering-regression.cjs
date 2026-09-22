const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
const source = fs.readFileSync('web/static/js/library.js', 'utf8').replace(/^import .*;\n/gm, '').replace(/export /g, '');
async function main() {
const handlers = {};
let requests = 0;
let completePage;
const pageDone = new Promise(resolve => { completePage = resolve; });
const shell = { scrollTop: 0, clientHeight: 600, scrollHeight: 3000, addEventListener: (name, fn) => { handlers[name] = fn; } };
const view = {hidden:false};
const grid = { dataset: {}, querySelector:()=>null, addEventListener() {}, insertAdjacentHTML() {} };
const document = {getElementById: id => ({libraryView:view, gameGrid:grid})[id] || null, querySelector:()=>shell, documentElement:{scrollHeight:600}};
const context = vm.createContext({document, window:{innerWidth:390, innerHeight:600,scrollY:0,addEventListener(){}}, getLibraryRevision:()=>0,library:{list:async()=>{requests++; await pageDone; return {items:[],hasMore:false};}}, getCoverURL:()=>'/cover.jpg',formatYear:()=>'',releaseStatus:()=>'',releaseLabel:()=>''});
vm.runInContext(source, context);
vm.runInContext("paginationState.loading=true; paginationState.hasMore=true; renderPagedItems(document.getElementById('gameGrid'), [], false)", context);
assert.equal(vm.runInContext('paginationState.loading',context),false);
assert.equal(vm.runInContext('paginationState.hasMore',context),false);
vm.runInContext('paginationState.hasMore=true; attachScrollListener()',context);
handlers.scroll();
assert.equal(requests,0,'scrolling near shell top must not fall through to short document');
view.hidden=true; shell.scrollTop=2400; handlers.scroll();
assert.equal(requests,0,'hidden library must not paginate');
view.hidden=false; handlers.scroll();
assert.equal(requests,1,'visible library near bottom paginates');
completePage();
await pageDone;
await new Promise(resolve => setImmediate(resolve));
assert.equal(vm.runInContext('paginationState.loading',context),false, 'pagination request completed');
const cards=vm.runInContext("paginationState.offset=0; buildCardHTML(Array.from({length:5}, (_,i)=>({game_id:i,game_name:'Game',status:'backlog'})))",context);
assert.equal((cards.match(/loading="eager"/g)||[]).length,2);
assert.equal((cards.match(/fetchpriority="high"/g)||[]).length,2);
const nextPage=vm.runInContext("paginationState.offset=60; buildCardHTML([{game_id:6,game_name:'Game',status:'backlog'}])",context);
assert(!nextPage.includes('fetchpriority="high"'));
// Exercise route reuse independently of the DOM renderer. The renderer's actual
// route-key assignment is evaluated below, so selected filters cannot become identity.
vm.runInContext(`ensureLibFilterFab = () => {}; ensureStatusFilterFab = () => {};
  loadLibrary = async status => { globalThis.loadedStatus = status; globalThis.reloads++; };
  globalThis.reloads = 0; libraryRoute = 'library/';`, context);
const assignment = source.match(/renderedRoute = libraryRoute;/)?.[0];
assert(assignment, 'library renderer must use route identity, not selected filters');
for (const statuses of [['backlog'], ['backlog', 'playing']]) {
  context.selected = statuses;
  vm.runInContext(`paginationState.statuses = selected; ${assignment} renderedRevision = 0;`, context);
  await vm.runInContext("resumeLibraryRoute(null, '')", context);
  assert.equal(context.reloads, 0, 'unchanged #library retains single/multi filters and DOM');
  assert.deepEqual([...vm.runInContext('paginationState.statuses', context)], statuses);
}
await vm.runInContext("resumeLibraryRoute(null, 'completed')", context);
assert.equal(context.loadedStatus, 'completed', 'explicit status deep links apply their filter');
vm.runInContext("renderedRoute = 'library/completed'; renderedRevision = -1", context);
await vm.runInContext("resumeLibraryRoute(null, 'completed')", context);
assert.equal(context.loadedStatus, undefined, 'mutation reload retains local filters');

// Production save queue: two overlapping flushes serialize writes, and the
// second captures edits made while the first request was pending.
const saveQueue = source.slice(source.indexOf('  let pendingSave = null;'), source.indexOf('  const saveCurrent = async'));
let releaseSave;
let writes = 0;
const saving = vm.createContext({Promise, saveCurrent: async () => {
  writes++;
  if (writes === 1) await new Promise(resolve => { releaseSave = resolve; });
}});
vm.runInContext(saveQueue, saving);
const first = vm.runInContext('runSave()', saving);
const second = vm.runInContext('runSave()', saving);
await Promise.resolve();
assert.equal(writes, 1, 'overlapping autosave/close cannot write concurrently');
releaseSave();
await Promise.all([first, second]);
assert.equal(writes, 2);
assert.equal(vm.runInContext('pendingSave', saving), null);
const closeSource = source.slice(source.indexOf('  let closing = null;'), source.indexOf('  activeModalClose = close;'));
let removals = 0;
let refreshes = 0;
let failSave = true;
const closingContext = vm.createContext({
  Promise, clearTimeout() {}, dismissTagMenu() {}, pendingSave: null,
  unsavedChanges: true, saveTimer: 1, flashTimer: null, activeModalClose: null,
  paginationState: {mode:'library'}, savedAtLeastOnce: true,
  createdYet: true, collectPayload: () => ({status:'backlog'}),
  modal: {remove() { removals++; }},
  document: {body:{classList:{remove(){}}}, removeEventListener(){}},
  window: {location:{hash:'#stats',pathname:'/',search:''}},
  history: {replaceState(){}}, id: 1, prevHash: '#now', prevHashWasGame: false,
  escHandler() {}, refreshVisibleLibrary: async () => { refreshes++; },
  runSave: async () => { if (!failSave) closingContext.unsavedChanges = false; },
});
vm.runInContext(closeSource, closingContext);
assert.equal(await vm.runInContext('close()', closingContext), false);
assert.equal(removals, 0, 'failed save keeps modal and event ownership');
failSave = false;
assert.equal(await vm.runInContext('close()', closingContext), true);
assert.equal(removals, 1);
assert.equal(refreshes, 1, 'successful edited modal refreshes visible view');
let hashRestores = 0;
closingContext.history.replaceState = () => { hashRestores++; };
closingContext.window.location.hash = '#game/2';
assert.equal(await vm.runInContext('close(true)', closingContext), true);
assert.equal(hashRestores, 0, 'route-driven close preserves incoming game deep link');
closingContext.createdYet = false;
closingContext.collectPayload = () => ({status:'', notes:'Draft notes', rating:4});
closingContext.unsavedChanges = true;
closingContext.saveTimer = 1;
closingContext.savedAtLeastOnce = false;
closingContext.runSave = async () => { throw new Error('statusless draft must not save'); };
const beforeCancel = removals;
assert.equal(await vm.runInContext('close()', closingContext), true);
assert.equal(removals, beforeCancel + 1, 'uncreated statusless draft can be cancelled');
assert.equal(closingContext.saveTimer, null);
assert.equal(closingContext.unsavedChanges, false);
closingContext.createdYet = false;
closingContext.unsavedChanges = true;
closingContext.pendingSave = Promise.resolve().then(() => { closingContext.createdYet = true; });
closingContext.runSave = async () => {};
assert.equal(await vm.runInContext('close()', closingContext), false,
  'draft cancellation must wait for an in-flight creation before deciding ownership');
assert.equal(removals, beforeCancel + 1, 'created entry retains unsaved edits');
console.log('PASS: pagination completion, shell scrolling, cover priorities, route/filter cache, serialized modal saves and failed-close retry');
}
const watchdog = setTimeout(() => { console.error('FAIL: regression did not complete'); process.exitCode = 1; }, 5000);
main().then(() => clearTimeout(watchdog), error => { clearTimeout(watchdog); console.error(error); process.exitCode = 1; });
