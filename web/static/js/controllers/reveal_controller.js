import { Controller } from "@hotwired/stimulus"

// Shows a section only while a checkbox or radio in the form is checked.
// Hidden sections have their inputs disabled so they aren't submitted.
//
//   <form data-controller="reveal" data-action="change->reveal#update">
//     <input type="checkbox" name="reusable" value="1">
//     <div data-reveal-target="section" data-reveal-when="reusable=1">…</div>
//
// data-reveal-when is "name=value"; several may be separated by spaces.
export default class extends Controller {
  static targets = ["section"]

  connect() {
    this.update()
  }

  update() {
    this.sectionTargets.forEach((section) => {
      const show = section.dataset.revealWhen.split(/\s+/).some((cond) => {
        const [name, value] = cond.split("=")
        const input = this.element.querySelector(`[name="${name}"][value="${value}"]`)
        return input?.checked
      })
      section.hidden = !show
      section.querySelectorAll("input, select, textarea").forEach((el) => (el.disabled = !show))
    })
  }
}
