import { Controller } from "@hotwired/stimulus"

// Shows how many dates are still waiting to go to the calendar. While
// any are, it asks the server every few seconds; it stops at zero.
//   <div data-controller="calendar-status" data-calendar-status-url-value="/…"
//        data-calendar-status-waiting-value="12">
export default class extends Controller {
  static targets = ["waiting", "sent", "error"]
  static values = { url: String, waiting: Number }

  connect() {
    this.stopped = false
    this.schedule()
  }

  disconnect() {
    this.stopped = true
    clearTimeout(this.timer)
  }

  schedule() {
    clearTimeout(this.timer)
    if (this.stopped || this.waitingValue <= 0) return
    this.timer = setTimeout(() => this.poll(), 5000)
  }

  async poll() {
    try {
      const resp = await fetch(this.urlValue, { headers: { Accept: "application/json" }, credentials: "same-origin" })
      if (!resp.ok) throw new Error(String(resp.status))
      const data = await resp.json()
      if (this.stopped) return
      this.waitingValue = data.waiting
      this.waitingTarget.textContent = String(data.waiting)
      this.sentTarget.textContent = data.last_sent
      this.errorTarget.textContent = data.last_error ? "Last error: " + data.last_error : ""
      this.errorTarget.hidden = !data.last_error
    } catch (e) {
      // Try again at the next tick.
    }
    this.schedule()
  }
}
