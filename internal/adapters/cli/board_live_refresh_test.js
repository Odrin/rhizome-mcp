const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

function extractBoardLiveRefreshScript() {
  const source = fs.readFileSync(path.join(__dirname, "board_html.go"), "utf8");
  const match = source.match(/const boardLiveRefreshScript = `([\s\S]*?)`/);
  if (!match) {
    throw new Error("boardLiveRefreshScript not found");
  }
  return match[1];
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
