const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

function extractBoardSearchScript() {
  return fs.readFileSync(path.join(__dirname, "board_assets", "board_search.js"), "utf8");
}

class FakeElement {
  constructor(tagName, attrs = {}) {
    this.tagName = tagName.toUpperCase();
    this.attributes = { ...attrs };
    this.children = [];
    this.parentNode = null;
    this.style = {};
    this.hidden = false;
    this.open = false;
    this._textContent = "";
    this.id = attrs.id || "";
    this.listeners = {};
    this.isFocused = false;
    this.dataset = {};
    this.ownerDocument = null;
  }

  get textContent() {
    if (this.children.length === 0) {
      return this._textContent;
    }
    return this.children.map((child) => child.textContent).join("");
  }

  set textContent(value) {
    this._textContent = String(value);
    this.children = [];
  }

  setAttribute(name, value) {
    this.attributes[name] = String(value);
    if (name === "id") this.id = String(value);
  }

  getAttribute(name) {
    return this.attributes[name] ?? null;
  }

  appendChild(child) {
    child.parentNode = this;
    child.ownerDocument = this.ownerDocument || this;
    this.children.push(child);
    this._textContent = "";
  }

  insertBefore(child, ref) {
    child.parentNode = this;
    child.ownerDocument = this.ownerDocument || this;
    const index = this.children.indexOf(ref);
    if (index >= 0) this.children.splice(index, 0, child);
    else this.children.push(child);
    this._textContent = "";
  }

  replaceChildren(...children) {
    this.children = [];
    for (const child of children) {
      if (child == null) continue;
      child.parentNode = this;
      child.ownerDocument = this.ownerDocument || this;
      this.children.push(child);
    }
    this._textContent = "";
  }

  focus() {
    this.isFocused = true;
    if (this.ownerDocument) this.ownerDocument.activeElement = this;
  }

  addEventListener(event, handler) {
    this.listeners[event] ||= [];
    this.listeners[event].push(handler);
  }

  dispatchEvent(event) {
    for (const handler of this.listeners[event] || []) handler();
  }

  querySelector(selector) {
    if (selector === "input[name=search]") {
      return this.findDescendant((child) => child.tagName === "INPUT" && child.getAttribute("name") === "search");
    }
    if (selector === "form") {
      return this.findDescendant((child) => child.tagName === "FORM");
    }
    if (selector === "form[data-board-search-form]") {
      return this.findDescendant((child) => child.tagName === "FORM" && child.getAttribute("data-board-search-form") !== null);
    }
    if (selector === "button[data-board-search-load-more]") {
      return this.findDescendant((child) => child.tagName === "BUTTON" && child.getAttribute("data-board-search-load-more") !== null);
    }
    if (selector === "[data-board-search-results]") {
      return this.findDescendant((child) => child.getAttribute("data-board-search-results") !== null);
    }
    if (selector === "[data-board-search-status]") {
      return this.findDescendant((child) => child.getAttribute("data-board-search-status") !== null);
    }
    if (selector === "[data-board-search-result]") {
      return this.findDescendant((child) => child.getAttribute("data-board-search-result") !== null);
    }
    if (selector === "button[type=submit]") {
      return this.findDescendant((child) => child.tagName === "BUTTON" && child.getAttribute("type") === "submit");
    }
    if (selector.startsWith("#")) {
      const id = selector.slice(1);
      return this.findDescendant((child) => child.id === id);
    }
    return null;
  }

  querySelectorAll(selector) {
    const results = [];
    this.walk((child) => {
      if (selector === "button" && child.tagName === "BUTTON") results.push(child);
      if (selector === "[data-board-search-result]" && child.getAttribute("data-board-search-result") !== null) results.push(child);
      if (selector === "button[type=submit]" && child.tagName === "BUTTON" && child.getAttribute("type") === "submit") results.push(child);
    });
    return results;
  }

  findDescendant(predicate) {
    let match = null;
    this.walk((child) => {
      if (!match && predicate(child)) match = child;
    });
    return match;
  }

  walk(visitor) {
    for (const child of this.children) {
      visitor(child);
      child.walk(visitor);
    }
  }
}

FakeElement.prototype.className = "";

class FakeHistory {
  constructor() {
    this.state = null;
    this.entries = [];
    this.index = -1;
  }

  pushState(state, _, url) {
    this.state = state;
    this.entries.push({ url, state });
    this.index = this.entries.length - 1;
  }

  replaceState(state, _, url) {
    this.state = state;
    if (this.entries.length === 0) this.entries.push({ url, state });
    else this.entries[this.index] = { url, state };
  }
}

function createHarness({ fetchResponses = [], initialQuery = "" } = {}) {
  const document = new FakeElement("document");
  document.visibilityState = "visible";
  document.activeElement = null;
  document.createElement = (tagName) => new FakeElement(tagName);
  const history = new FakeHistory();
  const fetchCalls = [];
  const timers = new Map();
  const abortControllers = [];
  let nextTimerId = 1;
  let now = 1_700_000_000_000;
  const state = { fetchResponses };
  const fetchImpl = async (url, options = {}) => {
    fetchCalls.push({ url, options });
    const queue = Array.isArray(state.fetchResponses) ? [...state.fetchResponses] : [];
    const response = queue.shift() || { status: 200, ok: true, headers: { get: () => "" }, json: async () => ({ results: [], next_cursor: null, has_more: false }) };
    if (Array.isArray(state.fetchResponses)) {
      state.fetchResponses = queue;
    }
    return {
      ok: response.ok ?? true,
      status: response.status ?? 200,
      headers: response.headers ?? { get: () => "" },
      json: async () => response.json ? response.json() : response.body,
      abort: () => {},
    };
  };
  const setTimeoutImpl = (fn, delay) => {
    const id = nextTimerId++;
    timers.set(id, { fn, delay });
    return id;
  };
  const clearTimeoutImpl = (id) => {
    timers.delete(id);
  };
  const context = {
    window: { history, location: { href: "http://example.test/search?q=" + encodeURIComponent(initialQuery), search: "" }, addEventListener: () => {} },
    document,
    fetch: fetchImpl,
    setTimeout: setTimeoutImpl,
    clearTimeout: clearTimeoutImpl,
    URLSearchParams,
    URL,
    AbortController: class AbortController {
      constructor() {
        this.signal = { aborted: false };
        abortControllers.push(this);
      }
      abort() {
        this.signal.aborted = true;
      }
    },
    console,
    Math,
    Date,
    DOMParser: class {},
    globalThis: null,
  };
  context.globalThis = context;
  return { context, document, history, fetchCalls, fetchImpl, setTimeoutImpl, clearTimeoutImpl, timers, state, abortControllers };
}

function loadClient(scriptSource, harness) {
  const context = vm.createContext(harness.context);
  vm.runInContext(scriptSource, context, { filename: "board_html.go" });
  return context.window.__rhizomeBoardSearchTestHooks?.BoardSearchClient;
}

test("renders safe FTS markers, aborts stale responses, and updates history state", async () => {
  const harness = createHarness({ initialQuery: "alpha" });
  harness.context.window.__rhizomeBoardSearchTestHooks = {};
  const scriptSource = extractBoardSearchScript();
  const context = vm.createContext(harness.context);
  vm.runInContext(scriptSource, context, { filename: "board_html.go" });
  const Client = context.window.__rhizomeBoardSearchTestHooks?.BoardSearchClient;
  assert.ok(Client);

  const root = new FakeElement("div");
  root.ownerDocument = harness.document;
  const form = new FakeElement("form", { "data-board-search-form": "" });
  const input = new FakeElement("input", { name: "search" });
  input.ownerDocument = harness.document;
  const results = new FakeElement("div", { "data-board-search-results": "" });
  const status = new FakeElement("div", { "data-board-search-status": "" });
  form.ownerDocument = harness.document;
  results.ownerDocument = harness.document;
  status.ownerDocument = harness.document;
  form.appendChild(input);
  root.appendChild(form);
  root.appendChild(results);
  root.appendChild(status);
  harness.document.children = [root];
  harness.state.fetchResponses = [
    { status: 200, ok: true, headers: { get: () => "" }, json: async () => ({ results: [{ entity_type: "issue", entity_id: "1", issue_id: "ISSUE-1", title: "Alpha", snippet: "one [alpha] two", score: 1 }], next_cursor: "cursor-1", has_more: true }) },
    { status: 200, ok: true, headers: { get: () => "" }, json: async () => ({ results: [{ entity_type: "issue", entity_id: "1", issue_id: "ISSUE-1", title: "Alpha", snippet: "one [alpha] two", score: 1 }], next_cursor: "cursor-1", has_more: true }) },
  ];
  const client = new Client(root, {
    fetch: harness.fetchImpl,
    document: harness.document,
    window: harness.context.window,
    history: harness.history,
  });

  client.applyLocationState("alpha", "issue");
  await client.runSearch("alpha", { entityType: "issue" });
  const resultItem = results.querySelector("[data-board-search-result]");
  assert.ok(resultItem);
  assert.equal(resultItem.children[0].textContent, "Alpha");
  assert.equal(harness.history.entries.at(-1).url, "/search?q=alpha&entity_type=issue");

  const malformedSnippet = "[ok] [nested [broken]] [ ] [x]";
  const snippetTarget = new FakeElement("div");
  snippetTarget.ownerDocument = harness.document;
  client.renderSnippet(snippetTarget, malformedSnippet);
  assert.equal(snippetTarget.textContent, malformedSnippet);

  const firstRequest = { abort: () => {} };
  const secondRequest = { abort: () => {} };
  client.fetchImpl = async () => firstRequest;
  const firstSearch = client.runSearch("beta", { entityType: "issue" });
  client.fetchImpl = async () => secondRequest;
  await client.runSearch("gamma", { entityType: "issue" });
  await firstSearch;
  assert.ok(harness.abortControllers[0]?.signal?.aborted);
});

test("handles empty, error, and paginated append states without mixing cursors", async () => {
  const harness = createHarness();
  harness.context.window.__rhizomeBoardSearchTestHooks = {};
  const scriptSource = extractBoardSearchScript();
  const context = vm.createContext(harness.context);
  vm.runInContext(scriptSource, context, { filename: "board_html.go" });
  const Client = context.window.__rhizomeBoardSearchTestHooks?.BoardSearchClient;
  assert.ok(Client);

  const root = new FakeElement("div");
  root.ownerDocument = harness.document;
  const form = new FakeElement("form", { "data-board-search-form": "" });
  const input = new FakeElement("input", { name: "search" });
  const results = new FakeElement("div", { "data-board-search-results": "" });
  const status = new FakeElement("div", { "data-board-search-status": "" });
  form.ownerDocument = harness.document;
  results.ownerDocument = harness.document;
  status.ownerDocument = harness.document;
  form.appendChild(input);
  root.appendChild(form);
  root.appendChild(results);
  root.appendChild(status);
  harness.document.children = [root];
  harness.state.fetchResponses = [
    { status: 500, ok: false, headers: { get: () => "" }, json: async () => ({ error: "boom" }) },
    { status: 200, ok: true, headers: { get: () => "" }, json: async () => ({ results: [{ entity_type: "issue", entity_id: "1", issue_id: "ISSUE-1", title: "One", snippet: "one", score: 1 }], next_cursor: "cursor-1", has_more: true }) },
    { status: 200, ok: true, headers: { get: () => "" }, json: async () => ({ results: [{ entity_type: "issue", entity_id: "1", issue_id: "ISSUE-1", title: "One", snippet: "one", score: 1 }], next_cursor: "cursor-1", has_more: true }) },
  ];
  const client = new Client(root, {
    fetch: harness.fetchImpl,
    document: harness.document,
    window: harness.context.window,
    history: harness.history,
  });

  await client.runSearch("", { append: false });
  assert.match(status.textContent, /initial/i);

  await client.runSearch("broken", { append: false });
  assert.match(status.textContent, /error/i);
  assert.equal(results.textContent, "");

  await client.runSearch("one", { append: false });
  await client.runSearch("one", { append: true, cursor: "cursor-1" });
  assert.equal(client.activeCursor, "cursor-1");
  assert.ok(client.results.length >= 1);
});

test("default window fetch is bound to window to avoid illegal invocation", async () => {
  const harness = createHarness();
  harness.context.window.__rhizomeBoardSearchTestHooks = {};
  const scriptSource = extractBoardSearchScript();
  const context = vm.createContext(harness.context);
  vm.runInContext(scriptSource, context, { filename: "board_html.go" });
  const Client = context.window.__rhizomeBoardSearchTestHooks?.BoardSearchClient;
  assert.ok(Client);

  const root = new FakeElement("div");
  root.ownerDocument = harness.document;
  const form = new FakeElement("form", { "data-board-search-form": "" });
  const input = new FakeElement("input", { name: "search" });
  const submit = new FakeElement("button", { type: "submit" });
  const results = new FakeElement("div", { "data-board-search-results": "" });
  const status = new FakeElement("div", { "data-board-search-status": "" });
  form.ownerDocument = harness.document;
  input.ownerDocument = harness.document;
  submit.ownerDocument = harness.document;
  results.ownerDocument = harness.document;
  status.ownerDocument = harness.document;
  form.appendChild(input);
  form.appendChild(submit);
  root.appendChild(form);
  root.appendChild(results);
  root.appendChild(status);
  harness.document.children = [root];

  let invoked = false;
  harness.context.window.fetch = async function () {
    if (this !== harness.context.window) {
      throw new TypeError("Illegal invocation");
    }
    invoked = true;
    return {
      ok: true,
      status: 200,
      headers: { get: () => "" },
      json: async () => ({ results: [], next_cursor: null, has_more: false }),
    };
  };

  const client = new Client(root, {
    fetch: harness.context.window.fetch,
    document: harness.document,
    window: harness.context.window,
    history: harness.history,
  });

  await client.runSearch("alpha", { append: false });
  assert.equal(invoked, true);
  assert.match(results.textContent, /no results/i);
});
