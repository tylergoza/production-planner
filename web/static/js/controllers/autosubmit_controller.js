import { Controller } from "@hotwired/stimulus"

// Submits the form when a control changes (filters, selects, search boxes).
export default class extends Controller {
  static values = { delay: { type: Number, default: 350 } }

  submit() {
    this.element.requestSubmit()
  }

  debounced() {
    clearTimeout(this.timer)
    this.timer = setTimeout(() => this.submit(), this.delayValue)
  }

  disconnect() {
    clearTimeout(this.timer)
  }
}
