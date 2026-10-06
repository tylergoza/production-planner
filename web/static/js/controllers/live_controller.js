import { Controller } from "@hotwired/stimulus"

// Keeps a page current while it's open. The server sends "change" on
// /live (Server-Sent Events) whenever anyone saves something; this
// re-fetches the page and swaps in the new region.
//
// It never throws away what someone is typing: while a field in the region
// has focus it waits until focus leaves, and once a form has been edited it
// shows the notice instead and only swaps when asked.
//
// Phones freeze installed apps in the background and drop the connection,
// so coming back to the page reconnects if needed and catches up.
const STALE = 60_000 // no ping for this long means the connection is dead

export default class extends Controller {
  static targets = ["region", "notice"]

  connect() {
    this.dirty = false
    this.pending = false
    this.onInput = (event) => {
      if (event.target.closest("form[method='post']")) this.dirty = true
    }
    this.onFocusOut = () => setTimeout(() => this.pending && this.refresh())
    this.onVisible = () => this.wake()
    this.onPageShow = (event) => event.persisted && this.wake()
    this.element.addEventListener("input", this.onInput)
    this.element.addEventListener("change", this.onInput)
    this.element.addEventListener("focusout", this.onFocusOut)
    document.addEventListener("visibilitychange", this.onVisible)
    window.addEventListener("pageshow", this.onPageShow)
    window.addEventListener("online", this.onVisible)
    this.open()
  }

  disconnect() {
    clearTimeout(this.timer)
    this.source?.close()
    this.element.removeEventListener("input", this.onInput)
    this.element.removeEventListener("change", this.onInput)
    this.element.removeEventListener("focusout", this.onFocusOut)
    document.removeEventListener("visibilitychange", this.onVisible)
    window.removeEventListener("pageshow", this.onPageShow)
    window.removeEventListener("online", this.onVisible)
  }

  // "Show them" on the notice: drop the edits and load the changes.
  apply() {
    this.dirty = false
    this.refresh()
  }

  open() {
    this.source?.close()
    this.seen = Date.now()
    let reconnecting = false
    this.source = new EventSource("/live")
    this.source.onmessage = (event) => {
      this.seen = Date.now()
      if (event.data === "change") this.schedule()
    }
    // A reconnect may have missed changes in between.
    this.source.onopen = () => reconnecting && this.schedule()
    this.source.onerror = () => (reconnecting = true)
  }

  wake() {
    if (document.visibilityState !== "visible") return
    if (this.source.readyState === EventSource.CLOSED || Date.now() - this.seen > STALE) {
      this.open()
      this.schedule()
    } else if (this.pending) {
      this.schedule()
    }
  }

  // Several saves in a row cause one refresh.
  schedule() {
    clearTimeout(this.timer)
    this.timer = setTimeout(() => this.refresh(), 300)
  }

  async refresh() {
    if (document.visibilityState !== "visible" || this.#busy()) {
      this.pending = true
      this.noticeTarget.hidden = !this.dirty
      return
    }
    this.pending = false
    let response
    try {
      response = await fetch(location.href, { headers: { "X-Live-Refresh": "1" } })
    } catch {
      return // offline; the next change or wake-up tries again
    }
    // Signed out (sent to the login page), deleted (404) and so on: leave
    // the page as it is rather than swap in something else.
    if (!response.ok || response.redirected) return
    const html = await response.text()
    if (this.#busy()) {
      this.pending = true
      this.noticeTarget.hidden = !this.dirty
      return
    }
    const doc = new DOMParser().parseFromString(html, "text/html")
    const fresh = doc.querySelector("[data-live-target='region']")
    if (!fresh) return

    const open = new Set([...this.regionTarget.querySelectorAll("details[open][data-live-key]")].map((d) => d.dataset.liveKey))
    this.regionTarget.replaceChildren(...fresh.childNodes)
    this.regionTarget.querySelectorAll("details[data-live-key]").forEach((d) => (d.open = open.has(d.dataset.liveKey)))
    document.title = doc.title
    this.dirty = false
    this.noticeTarget.hidden = true
  }

  #busy() {
    if (this.dirty) return true
    const el = document.activeElement
    return this.regionTarget.contains(el) && el.matches("input, select, textarea")
  }
}
