const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

function extractBoardLiveRefreshScript() {
  return fs.readFileSync(path.join(__dirname, "board_assets", "board_live.js"), "utf8");
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
    this.textContent = "";
    this.id = attrs.id || "";
    this._innerHTML = "";
    this.listeners = {};
    this.isFocused = false;
    this.dataset = {};
    this.ownerDocument = null;
  }

  setAttribute(name, value) {
    this.attributes[name] = String(value);
    if (name === "id") {
      this.id = String(value);
    }
    if (name === "data-board-main") {
      this.dataset["board-main"] = String(value);
    }
  }

  getAttribute(name) {
    return this.attributes[name] ?? null;
  }

  appendChild(child) {
    child.parentNode = this;
    child.ownerDocument = this.ownerDocument;
    this.children.push(child);
  }

  insertBefore(child, ref) {
    child.parentNode = this;
    child.ownerDocument = this.ownerDocument;
    const index = this.children.indexOf(ref);
    if (index >= 0) {
      this.children.splice(index, 0, child);
    } else {
      this.children.push(child);
    }
  }

  focus() {
    this.isFocused = true;
    if (this.ownerDocument) {
      this.ownerDocument.activeElement = this;
    }
  }

  addEventListener(event, handler) {
    this.listeners[event] ||= [];
    this.listeners[event].push(handler);
  }

  dispatchEvent(event) {
    for (const handler of this.listeners[event] || []) {
      handler();
    }
  }

  querySelector(selector) {
    if (selector === "main[data-board-main]") {
      return this.findDescendant((child) => child.tagName === "MAIN" && child.getAttribute("data-board-main") !== null);
    }
    if (selector === "details[id]") {
      return this.findDescendant((child) => child.tagName === "DETAILS" && child.id !== "");
    }
    if (selector.startsWith("#")) {
      const id = selector.slice(1);
      return this.findDescendant((child) => child.id === id);
    }
    return null;
  }

  querySelectorAll(selector) {
    const results = [];
    if (selector === "details[id]") {
      this.walk((child) => {
        if (child.tagName === "DETAILS" && child.id !== "") {
          results.push(child);
        }
      });
      return results;
    }
    return results;
  }

  findDescendant(predicate) {
    let match = null;
    this.walk((child) => {
      if (!match && predicate(child)) {
        match = child;
      }
    });
    return match;
  }

  walk(visitor) {
    for (const child of this.children) {
      visitor(child);
      child.walk(visitor);
    }
  }

  get innerHTML() {
    return this._innerHTML;
  }

  set innerHTML(value) {
    this._innerHTML = value;
    this.children = [];
    if (!value) {
      return;
    }
    const stack = [this];
    const tagRegex = /<\/?([a-zA-Z0-9-]+)([^>]*)>/g;
    let match;
    while ((match = tagRegex.exec(value)) !== null) {
      const full = match[0];
      const tagName = match[1].toLowerCase();
      const attrsText = match[2] || "";
      const isClosing = full.startsWith("</");
      if (isClosing) {
        if (stack.length > 1) {
          stack.pop();
        }
        continue;
      }
      const attrs = {};
      const attrRegex = /([a-zA-Z0-9:-]+)(?:=(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?/g;
      let attrMatch;
      while ((attrMatch = attrRegex.exec(attrsText)) !== null) {
        const name = attrMatch[1];
        const attrValue = attrMatch[2] ?? attrMatch[3] ?? attrMatch[4] ?? "";
        attrs[name] = attrValue === "" ? true : attrValue;
      }
      const element = new FakeElement(tagName, attrs);
      element.ownerDocument = this.ownerDocument;
      if (attrs.open !== undefined) {
        element.open = true;
      }
      const parent = stack[stack.length - 1];
      parent.appendChild(element);
      if (!full.endsWith("/>") && tagName !== "input" && tagName !== "img" && tagName !== "meta" && tagName !== "link" && tagName !== "hr" && tagName !== "br") {
        stack.push(element);
      }
    }
  }
}

class FakeDOMParser {
  parseFromString(body) {
    const document = new FakeElement("document");
    const main = new FakeElement("main", { id: "parsed-main", "data-board-main": "" });
    main.ownerDocument = document;
    main.innerHTML = body;
    document.querySelector = (selector) => {
      if (selector === "main[data-board-main]") {
        return main;
      }
      return null;
    };
    document.querySelectorAll = (selector) => {
      if (selector === "details[id]") {
        return main.querySelectorAll(selector);
      }
      return [];
    };
    return document;
  }
}

function createHarness({ initialTestHooks = null, randomValue = 0.2, boardResponses = [], pageResponses = [] } = {}) {
  const timers = new Map();
  let nextTimerId = 1;
  let now = 1_700_000_000_000;
  const fetchCalls = [];
  const timerDelays = [];

  const document = new FakeElement("document");
  document.visibilityState = "visible";
  document.activeElement = null;
  document.listeners = {};
  document.addEventListener = (event, handler) => {
    document.listeners[event] ||= [];
    document.listeners[event].push(handler);
  };
  document.createElement = (tagName) => {
    const element = new FakeElement(tagName);
    element.ownerDocument = document;
    return element;
  };
  document.ownerDocument = document;

  const window = {
    scrollY: 0,
    scrollTo: (x, y) => {
      window.scrollY = y;
    },
    addEventListener: () => {},
    __rhizomeBoardLiveClient: undefined,
    __rhizomeBoardLiveTestHooks: initialTestHooks,
  };
  window.listeners = {};
  window.addEventListener = (event, handler) => {
    window.listeners[event] ||= [];
    window.listeners[event].push(handler);
  };

  const setTimeoutImpl = (fn, delay) => {
    const id = nextTimerId++;
    const deadline = now + delay;
    timers.set(id, { id, deadline, fn, active: true });
    timerDelays.push(delay);
    return id;
  };
  const clearTimeoutImpl = (id) => {
    const timer = timers.get(id);
    if (timer) {
      timer.active = false;
      timers.delete(id);
    }
  };

  const fetchImpl = async (url, options = {}) => {
    const requestUrl = typeof url === "string" ? url : url.url;
    fetchCalls.push({ url: requestUrl, options });
    const response = requestUrl === "/api/board"
      ? (boardResponses.shift() || { status: 200, ok: true, headers: { get: () => "ETAG-1" }, text: "<main data-board-main></main>" })
      : (pageResponses.shift() || { status: 200, ok: true, headers: { get: () => "ETAG-1" }, text: "<main data-board-main></main>" });
    return {
      status: response.status ?? 200,
      ok: response.ok ?? true,
      headers: response.headers ?? { get: () => "ETAG-1" },
      text: async () => response.text ?? "",
    };
  };

  const randomImpl = () => randomValue;
  const nowImpl = () => new Date(now);

  const context = {
    window,
    document,
    DOMParser: FakeDOMParser,
    fetch: fetchImpl,
    setTimeout: setTimeoutImpl,
    clearTimeout: clearTimeoutImpl,
    Math,
    Date,
    console,
    globalThis: null,
  };
  context.globalThis = context;

  return {
    context,
    document,
    window,
    fetchCalls,
    timers,
    timerDelays,
    fetchImpl,
    setTimeoutImpl,
    clearTimeoutImpl,
    get now() {
      return now;
    },
    set now(value) {
      now = value;
    },
    nowImpl,
    randomImpl,
  };
}

function loadClient(scriptSource, harness) {
  const context = vm.createContext(harness.context);
  vm.runInContext(scriptSource, context, { filename: "board_html.go" });
  return context.window.__rhizomeBoardLiveTestHooks?.BoardLiveClient;
}

test("304 responses clear stale state and use the normal interval", async () => {
  const harness = createHarness({ initialTestHooks: {}, boardResponses: [
    { status: 200, ok: true, headers: { get: () => "ETAG-1" }, text: "<main data-board-main></main>" },
    { status: 304, ok: true, headers: { get: () => "ETAG-1" } },
  ], pageResponses: [{ status: 200, ok: true, headers: { get: () => "ETAG-1" }, text: "<main data-board-main></main>" }] });
  const scriptSource = extractBoardLiveRefreshScript();
  const root = new FakeElement("main", { id: "root-main", "data-board-main": "" });
  root.ownerDocument = harness.document;
  const initialChild = new FakeElement("div", { id: "initial" });
  root.appendChild(initialChild);
  harness.document.children = [root];
  harness.document.querySelector = (selector) => {
    if (selector === "main[data-board-main]") {
      return root;
    }
    return null;
  };
  harness.document.querySelectorAll = () => [];
  const Client = loadClient(scriptSource, harness);
  const client = new Client(root, {
    fetch: harness.fetchImpl,
    setTimeout: harness.setTimeoutImpl,
    clearTimeout: harness.clearTimeoutImpl,
    random: harness.randomImpl,
    now: harness.nowImpl,
    document: harness.document,
    window: harness.window,
    DOMParser: FakeDOMParser,
  });

  await client.refresh(false);
  await client.refresh(false);

  const boardFetches = harness.fetchCalls.filter(({ url }) => url === "/api/board");
  assert.equal(boardFetches[1].options.headers["If-None-Match"], "ETAG-1");
  assert.equal(client.staleStatus, null);
  assert.ok(client.lastSuccessfulRefreshAt);
  assert.equal(harness.timerDelays[harness.timerDelays.length - 1], 15000);
});

test("changed responses reconcile content and restore details, focus, and scroll", async () => {
  const harness = createHarness({ initialTestHooks: {}, boardResponses: [{ status: 200, ok: true, headers: { get: () => "ETAG-1" }, text: "<main data-board-main></main>" }], pageResponses: [{ status: 200, ok: true, headers: { get: () => "ETAG-1" }, text: "<main data-board-main><details id=\"open-one\" open></details><input id=\"focused\" /></main>" }] });
  const scriptSource = extractBoardLiveRefreshScript();
  const root = new FakeElement("main", { id: "root-main", "data-board-main": "" });
  root.ownerDocument = harness.document;
  const existingDetail = new FakeElement("details", { id: "open-one" });
  existingDetail.open = true;
  root.appendChild(existingDetail);
  const focusTarget = new FakeElement("input", { id: "focused" });
  focusTarget.ownerDocument = harness.document;
  root.appendChild(focusTarget);
  harness.document.children = [root];
  harness.document.querySelector = (selector) => {
    if (selector === "main[data-board-main]") {
      return root;
    }
    return null;
  };
  harness.document.querySelectorAll = () => [];
  harness.document.activeElement = focusTarget;
  harness.window.scrollY = 124;
  const Client = loadClient(scriptSource, harness);
  const client = new Client(root, {
    fetch: harness.fetchImpl,
    setTimeout: harness.setTimeoutImpl,
    clearTimeout: harness.clearTimeoutImpl,
    random: harness.randomImpl,
    now: harness.nowImpl,
    document: harness.document,
    window: harness.window,
    DOMParser: class {
      parseFromString(body) {
        const doc = new FakeElement("document");
        const main = new FakeElement("main", { id: "parsed-main", "data-board-main": "" });
        main.ownerDocument = doc;
        main.innerHTML = body;
        doc.querySelector = (selector) => {
          if (selector === "main[data-board-main]") {
            return main;
          }
          return null;
        };
        doc.querySelectorAll = () => [];
        return doc;
      }
    },
  });
  await client.refresh(false);

  assert.equal(root.querySelector("#focused")?.id, "focused");
  assert.equal(root.querySelectorAll("details[id]").length, 1);
  assert.equal(root.querySelectorAll("details[id]")[0].open, true);
  assert.equal(harness.document.activeElement?.id, "focused");
  assert.equal(harness.window.scrollY, 124);
  assert.ok(client.lastSuccessfulRefreshAt);
});

test("failed responses keep the old content, mark stale, and retry with jittered backoff", async () => {
  const harness = createHarness({ initialTestHooks: {}, randomValue: 0.2, boardResponses: [{ status: 500, ok: false, headers: { get: () => "ETAG-1" } }] });
  const scriptSource = extractBoardLiveRefreshScript();
  const root = new FakeElement("main", { id: "root-main", "data-board-main": "" });
  root.ownerDocument = harness.document;
  const existingChild = new FakeElement("div", { id: "keep-me" });
  root.appendChild(existingChild);
  harness.document.children = [root];
  harness.document.querySelector = (selector) => {
    if (selector === "main[data-board-main]") {
      return root;
    }
    return null;
  };
  harness.document.querySelectorAll = () => [];
  const Client = loadClient(scriptSource, harness);
  const client = new Client(root, {
    fetch: harness.fetchImpl,
    setTimeout: harness.setTimeoutImpl,
    clearTimeout: harness.clearTimeoutImpl,
    random: harness.randomImpl,
    now: harness.nowImpl,
    document: harness.document,
    window: harness.window,
    DOMParser: FakeDOMParser,
  });
  await client.refresh(false);

  assert.equal(root.querySelector("#keep-me")?.id, "keep-me");
  assert.equal(client.staleStatus, "stale");
  assert.equal(harness.timerDelays[harness.timerDelays.length - 1], 1700);
});

test("default window timers are bound to window", () => {
  const harness = createHarness({ initialTestHooks: {} });
  const scriptSource = extractBoardLiveRefreshScript();
  const root = new FakeElement("main", { id: "root-main", "data-board-main": "" });
  root.ownerDocument = harness.document;
  harness.document.children = [root];
  harness.document.querySelector = (selector) => {
    if (selector === "main[data-board-main]") {
      return root;
    }
    return null;
  };
  harness.document.querySelectorAll = () => [];

  let scheduled = false;
  harness.window.setTimeout = function (fn, delay) {
    if (this !== harness.window) {
      throw new TypeError("Illegal invocation");
    }
    scheduled = true;
    return harness.setTimeoutImpl(fn, delay);
  };
  harness.window.clearTimeout = function (id) {
    if (this !== harness.window) {
      throw new TypeError("Illegal invocation");
    }
    return harness.clearTimeoutImpl(id);
  };

  const Client = loadClient(scriptSource, harness);
  new Client(root, {
    document: harness.document,
    window: harness.window,
    DOMParser: FakeDOMParser,
  });

  assert.equal(scheduled, true);
});

test("a later success clears stale state and restores the normal interval", async () => {
  const harness = createHarness({ initialTestHooks: {}, randomValue: 0.2, boardResponses: [
    { status: 500, ok: false, headers: { get: () => "ETAG-1" } },
    { status: 200, ok: true, headers: { get: () => "ETAG-1" }, text: "<main data-board-main></main>" },
  ] });
  const scriptSource = extractBoardLiveRefreshScript();
  const root = new FakeElement("main", { id: "root-main", "data-board-main": "" });
  root.ownerDocument = harness.document;
  harness.document.children = [root];
  harness.document.querySelector = (selector) => {
    if (selector === "main[data-board-main]") {
      return root;
    }
    return null;
  };
  harness.document.querySelectorAll = () => [];
  const Client = loadClient(scriptSource, harness);
  const client = new Client(root, {
    fetch: harness.fetchImpl,
    setTimeout: harness.setTimeoutImpl,
    clearTimeout: harness.clearTimeoutImpl,
    random: harness.randomImpl,
    now: harness.nowImpl,
    document: harness.document,
    window: harness.window,
    DOMParser: FakeDOMParser,
  });
  await client.refresh(false);
  await client.refresh(false);

  assert.equal(client.staleStatus, null);
  assert.equal(harness.timerDelays[harness.timerDelays.length - 1], 15000);
});

test("visibility changes stop and restart the refresh loop", async () => {
  const harness = createHarness({ initialTestHooks: {}, boardResponses: [{ status: 200, ok: true, headers: { get: () => "ETAG-1" }, text: "<main data-board-main></main>" }] });
  const scriptSource = extractBoardLiveRefreshScript();
  const root = new FakeElement("main", { id: "root-main", "data-board-main": "" });
  root.ownerDocument = harness.document;
  harness.document.children = [root];
  harness.document.querySelector = (selector) => {
    if (selector === "main[data-board-main]") {
      return root;
    }
    return null;
  };
  harness.document.querySelectorAll = () => [];
  const Client = loadClient(scriptSource, harness);
  const client = new Client(root, {
    fetch: harness.fetchImpl,
    setTimeout: harness.setTimeoutImpl,
    clearTimeout: harness.clearTimeoutImpl,
    random: harness.randomImpl,
    now: harness.nowImpl,
    document: harness.document,
    window: harness.window,
    DOMParser: FakeDOMParser,
  });

  harness.document.visibilityState = "hidden";
  harness.document.dispatchEvent("visibilitychange");
  assert.equal(client.timer, null);

  harness.document.visibilityState = "visible";
  harness.document.dispatchEvent("visibilitychange");
  assert.equal(harness.fetchCalls.filter(({ url }) => url === "/api/board").length, 2);
});

class FakeEventSource {
  constructor(url) {
    this.url = url;
    this.readyState = 0;
    this.closed = false;
    this.listeners = new Map();
    FakeEventSource.instances.push(this);
  }
  addEventListener(type, fn) {
    if (!this.listeners.has(type)) {
      this.listeners.set(type, []);
    }
    this.listeners.get(type).push(fn);
  }
  dispatch(type, event = {}) {
    for (const fn of this.listeners.get(type) || []) {
      fn(event);
    }
  }
  close() {
    this.closed = true;
    this.readyState = 2;
  }
}
FakeEventSource.instances = [];

function createTestRoot(harness, attrs = {}) {
  const root = new FakeElement("main", { id: "root-main", "data-board-main": "", ...attrs });
  root.ownerDocument = harness.document;
  harness.document.children = [root];
  harness.document.querySelector = (selector) => (selector === "main[data-board-main]" ? root : null);
  harness.document.querySelectorAll = () => [];
  return root;
}

async function flushMicrotasks() {
  await new Promise((resolve) => setImmediate(resolve));
}

test("SSE ready and board_changed pause idle polling and refresh on demand", async () => {
  FakeEventSource.instances = [];
  const harness = createHarness({
    initialTestHooks: {},
    boardResponses: [
      { status: 200, ok: true, headers: { get: () => "ETAG-SSE-1" }, text: "<main data-board-main></main>" },
    ],
    pageResponses: [
      { status: 200, ok: true, headers: { get: () => "ETAG-SSE-1" }, text: "<div id=\"sse-updated\"></div>" },
    ],
  });
  harness.window.EventSource = FakeEventSource;
  const scriptSource = extractBoardLiveRefreshScript();
  const Client = loadClient(scriptSource, harness);
  const root = createTestRoot(harness, { "data-board-events-endpoint": "/api/events" });
  const client = new Client(root, {
    fetch: harness.fetchImpl,
    setTimeout: harness.setTimeoutImpl,
    clearTimeout: harness.clearTimeoutImpl,
    random: harness.randomImpl,
    now: harness.nowImpl,
    document: harness.document,
    window: harness.window,
    DOMParser: FakeDOMParser,
  });

  assert.equal(FakeEventSource.instances.length, 1);
  const es = FakeEventSource.instances[0];
  assert.equal(es.url, "/api/events");
  assert.equal(harness.timers.size, 0);

  es.readyState = 1;
  es.dispatch("ready");
  assert.equal(client.sseConnected, true);
  assert.equal(client.staleStatus, null);
  assert.equal(harness.fetchCalls.length, 0);

  client.markStale();
  es.dispatch("board_changed");
  await flushMicrotasks();

  assert.equal(harness.fetchCalls.filter(({ url }) => url === "/api/board").length, 1);
  assert.equal(client.staleStatus, null);
  assert.equal(client.timer, null);
  assert.equal(harness.timers.size, 0);
  assert.equal(root.children[0].getAttribute("id"), "sse-updated");

  client.close();
  assert.equal(es.closed, true);
  assert.equal(client.eventSource, null);
  assert.equal(client.timer, null);
});

test("SSE error falls back to exponential backoff polling until SSE reconnects", async () => {
  FakeEventSource.instances = [];
  const harness = createHarness({
    initialTestHooks: {},
    boardResponses: [
      { status: 304, ok: true, headers: { get: () => "ETAG-1" } },
    ],
  });
  const scriptSource = extractBoardLiveRefreshScript();
  const Client = loadClient(scriptSource, harness);
  const root = createTestRoot(harness);
  const client = new Client(root, {
    eventsEndpoint: "/custom/events",
    EventSource: FakeEventSource,
    fetch: harness.fetchImpl,
    setTimeout: harness.setTimeoutImpl,
    clearTimeout: harness.clearTimeoutImpl,
    random: harness.randomImpl,
    now: harness.nowImpl,
    document: harness.document,
    window: harness.window,
    DOMParser: FakeDOMParser,
  });

  const es = FakeEventSource.instances[0];
  assert.equal(es.url, "/custom/events");
  es.dispatch("error");
  assert.equal(client.sseConnected, false);
  assert.equal(client.staleStatus, "stale");
  assert.equal(harness.timers.size, 1);

  // Repeated SSE error while fallback timer is already scheduled does not duplicate timers.
  es.dispatch("error");
  assert.equal(harness.timers.size, 1);

  // A fallback poll succeeds while SSE is still degraded, restoring normal intervalMs polling.
  const scheduled = Array.from(harness.timers.values())[0];
  harness.timers.delete(scheduled.id);
  scheduled.fn();
  await flushMicrotasks();
  assert.equal(client.staleStatus, null);
  assert.equal(harness.timers.size, 1);
  assert.equal(harness.timerDelays[harness.timerDelays.length - 1], 15000);

  // Reconnect still catches up after a successful fallback poll.
  es.dispatch("open");
  assert.equal(client.sseConnected, true);
  assert.equal(client.timer, null);
  assert.equal(harness.timers.size, 0);
  await flushMicrotasks();
  assert.equal(harness.fetchCalls.filter(({ url }) => url === "/api/board").length, 2);
  assert.equal(harness.timers.size, 0);

  client.stop();
  assert.equal(es.closed, true);
});

test("SSE closes when page hidden and reconnects with fresh EventSource when visible", async () => {
  FakeEventSource.instances = [];
  const harness = createHarness({
    initialTestHooks: {},
    boardResponses: [{ status: 304, ok: true, headers: { get: () => "ETAG-1" } }],
  });
  const scriptSource = extractBoardLiveRefreshScript();
  const Client = loadClient(scriptSource, harness);
  const root = createTestRoot(harness);
  const client = new Client(root, {
    EventSource: FakeEventSource,
    fetch: harness.fetchImpl,
    setTimeout: harness.setTimeoutImpl,
    clearTimeout: harness.clearTimeoutImpl,
    random: harness.randomImpl,
    now: harness.nowImpl,
    document: harness.document,
    window: harness.window,
    DOMParser: FakeDOMParser,
  });

  const firstES = FakeEventSource.instances[0];
  firstES.dispatch("open");

  harness.document.visibilityState = "hidden";
  harness.document.dispatchEvent("visibilitychange");
  assert.equal(firstES.closed, true);
  assert.equal(client.eventSource, null);
  assert.equal(client.sseConnected, false);

  // Late events after close must be ignored cleanly.
  firstES.dispatch("board_changed");
  assert.equal(harness.fetchCalls.length, 0);

  harness.document.visibilityState = "visible";
  harness.document.dispatchEvent("visibilitychange");
  assert.equal(FakeEventSource.instances.length, 2);
  const secondES = FakeEventSource.instances[1];
  assert.equal(secondES.closed, false);

  await flushMicrotasks();
  assert.equal(harness.fetchCalls.filter(({ url }) => url === "/api/board").length, 1);

  secondES.dispatch("ready");
  assert.equal(client.timer, null);
  assert.equal(harness.timers.size, 0);
});

test("EventSource construction error falls back cleanly to periodic polling", async () => {
  class ThrowingEventSource {
    constructor() {
      throw new Error("blocked");
    }
  }
  const harness = createHarness({ initialTestHooks: {} });
  const scriptSource = extractBoardLiveRefreshScript();
  const Client = loadClient(scriptSource, harness);
  const root = createTestRoot(harness);
  const client = new Client(root, {
    EventSource: ThrowingEventSource,
    fetch: harness.fetchImpl,
    setTimeout: harness.setTimeoutImpl,
    clearTimeout: harness.clearTimeoutImpl,
    random: harness.randomImpl,
    now: harness.nowImpl,
    document: harness.document,
    window: harness.window,
    DOMParser: FakeDOMParser,
  });

  assert.equal(client.eventSource, null);
  assert.equal(harness.timers.size, 1);
  assert.equal(harness.timerDelays[0], 15000);
  client.stop();
  assert.equal(harness.timers.size, 0);
});

test("auto-bootstrapped client uses data-board-events-endpoint or /api/events", async () => {
  FakeEventSource.instances = [];
  const harness = createHarness({ initialTestHooks: {} });
  harness.window.EventSource = FakeEventSource;
  createTestRoot(harness, { "data-board-events-endpoint": "/api/events" });
  const scriptSource = extractBoardLiveRefreshScript();
  loadClient(scriptSource, harness);

  assert.equal(FakeEventSource.instances.length, 1);
  assert.equal(FakeEventSource.instances[0].url, "/api/events");
  harness.window.__rhizomeBoardLiveClient.stop();
  assert.equal(FakeEventSource.instances[0].closed, true);
});

test("SSE reconnect coalesces open and changed ready catch-up refreshes", async () => {
  FakeEventSource.instances = [];
  const harness = createHarness({
    initialTestHooks: {},
    boardResponses: [
      { status: 200, ok: true, headers: { get: () => "ETAG-REC-1" }, text: "<main data-board-main></main>" },
      { status: 200, ok: true, headers: { get: () => "ETAG-REC-2" }, text: "<main data-board-main></main>" },
    ],
    pageResponses: [
      { status: 200, ok: true, headers: { get: () => "ETAG-REC-1" }, text: "<div id=\"reconnected-open\"></div>" },
      { status: 200, ok: true, headers: { get: () => "ETAG-REC-2" }, text: "<div id=\"reconnected-watermark\"></div>" },
    ],
  });
  harness.window.EventSource = FakeEventSource;
  const scriptSource = extractBoardLiveRefreshScript();
  const Client = loadClient(scriptSource, harness);
  const root = createTestRoot(harness, { "data-board-events-endpoint": "/api/events" });
  const client = new Client(root, {
    fetch: harness.fetchImpl,
    setTimeout: harness.setTimeoutImpl,
    clearTimeout: harness.clearTimeoutImpl,
    random: harness.randomImpl,
    now: harness.nowImpl,
    document: harness.document,
    window: harness.window,
    DOMParser: FakeDOMParser,
  });

  const es = FakeEventSource.instances[0];
  es.readyState = 1;
  es.dispatch("open");
  es.dispatch("ready", { data: JSON.stringify({ watermark: "wm-1" }) });
  assert.equal(harness.fetchCalls.length, 0);

  // Simulate SSE interruption followed by EventSource reconnection.
  es.dispatch("error");
  assert.equal(client.sseConnected, false);
  assert.equal(client.staleStatus, "stale");

  // Reconnect "open" must clear stale status and trigger an immediate catch-up fetch.
  es.readyState = 1;
  es.dispatch("open");
  es.dispatch("ready", { data: JSON.stringify({ watermark: "wm-2" }) });
  await flushMicrotasks();
  assert.equal(client.sseConnected, true);
  assert.equal(client.staleStatus, null);
  assert.equal(harness.fetchCalls.filter(({ url }) => url === "/api/board").length, 1);
  assert.equal(root.children[0].getAttribute("id"), "reconnected-open");

  // Ready event carrying the unchanged watermark must not double-fetch.
  es.dispatch("ready", { data: JSON.stringify({ watermark: "wm-2" }) });
  await flushMicrotasks();
  assert.equal(harness.fetchCalls.filter(({ url }) => url === "/api/board").length, 1);

  // Ready event carrying a new watermark (missed update across server restart/reconnect) refreshes immediately.
  es.dispatch("ready", { data: JSON.stringify({ watermark: "wm-3" }) });
  await flushMicrotasks();
  assert.equal(harness.fetchCalls.filter(({ url }) => url === "/api/board").length, 2);
  assert.equal(root.children[0].getAttribute("id"), "reconnected-watermark");

  client.stop();
});

test("SSE changes during an HTML refresh queue a later refresh without overlapping requests", async () => {
  FakeEventSource.instances = [];
  const harness = createHarness({
    initialTestHooks: {},
    pageResponses: [
      { text: "<div id=\"older\"></div>" },
      { text: "<div id=\"newer\"></div>" },
    ],
  });
  let finishFirstPage;
  const firstPage = new Promise((resolve) => { finishFirstPage = resolve; });
  let pageCalls = 0;
  const fetchImpl = async (url, options) => {
    const response = await harness.fetchImpl(url, options);
    if (url === "/" && ++pageCalls === 1) {
      return { ...response, text: () => firstPage };
    }
    return response;
  };
  const Client = loadClient(extractBoardLiveRefreshScript(), harness);
  const root = createTestRoot(harness);
  const client = new Client(root, {
    EventSource: FakeEventSource,
    fetch: fetchImpl,
    setTimeout: harness.setTimeoutImpl,
    clearTimeout: harness.clearTimeoutImpl,
    document: harness.document,
    window: harness.window,
    DOMParser: FakeDOMParser,
  });
  const es = FakeEventSource.instances[0];
  es.dispatch("ready", { data: JSON.stringify({ watermark: "wm-1" }) });
  es.dispatch("board_changed", { data: JSON.stringify({ watermark: "wm-2" }) });
  await flushMicrotasks();
  es.dispatch("board_changed", { data: JSON.stringify({ watermark: "wm-3" }) });
  await flushMicrotasks();
  assert.equal(harness.fetchCalls.filter(({ url }) => url === "/api/board").length, 1);

  finishFirstPage("<div id=\"older\"></div>");
  await flushMicrotasks();
  assert.equal(harness.fetchCalls.filter(({ url }) => url === "/api/board").length, 2);
  assert.equal(root.children[0].id, "newer");
  client.stop();
});

for (const failure of ["api", "page"]) {
  test(`failed reconnect ${failure} fetch retries while SSE stays connected`, async () => {
    FakeEventSource.instances = [];
    const harness = createHarness({
      initialTestHooks: {},
      boardResponses: [
        { status: failure === "api" ? 503 : 200, ok: failure !== "api", headers: { get: () => "ETAG-NEW" } },
        { status: 200, ok: true, headers: { get: () => "ETAG-NEW" } },
      ],
      pageResponses: [
        ...(failure === "page" ? [{ status: 503, ok: false }] : []),
        { text: "<div id=\"caught-up\"></div>" },
      ],
    });
    const Client = loadClient(extractBoardLiveRefreshScript(), harness);
    const root = createTestRoot(harness);
    const client = new Client(root, {
      EventSource: FakeEventSource,
      fetch: harness.fetchImpl,
      setTimeout: harness.setTimeoutImpl,
      clearTimeout: harness.clearTimeoutImpl,
      document: harness.document,
      window: harness.window,
      DOMParser: FakeDOMParser,
      etag: "ETAG-OLD",
    });
    const es = FakeEventSource.instances[0];
    es.dispatch("ready", { data: JSON.stringify({ watermark: "wm-1" }) });
    es.dispatch("error");
    es.dispatch("open");
    es.dispatch("ready", { data: JSON.stringify({ watermark: "wm-2" }) });
    await flushMicrotasks();
    assert.equal(client.sseConnected, true);
    assert.equal(client.needsReconnectRefresh, true);
    assert.equal(client.staleStatus, "stale");
    assert.equal(harness.timers.size, 1);
    assert.equal(harness.fetchCalls.filter(({ url }) => url === "/api/board").length, 1);

    es.dispatch("ready", { data: JSON.stringify({ watermark: "wm-2" }) });
    assert.equal(client.staleStatus, "stale");
    assert.equal(harness.timers.size, 1);
    const scheduled = Array.from(harness.timers.values())[0];
    harness.timers.delete(scheduled.id);
    scheduled.fn();
    await flushMicrotasks();

    const boardFetches = harness.fetchCalls.filter(({ url }) => url === "/api/board");
    assert.equal(boardFetches.length, 2);
    assert.equal(boardFetches[1].options.headers["If-None-Match"], "ETAG-OLD");
    assert.equal(client.needsReconnectRefresh, false);
    assert.equal(client.staleStatus, null);
    assert.equal(client.etag, "ETAG-NEW");
    assert.equal(harness.timers.size, 0);
    assert.equal(root.children[0].id, "caught-up");
    client.stop();
  });
}
