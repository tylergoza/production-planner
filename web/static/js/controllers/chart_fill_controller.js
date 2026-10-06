import { Controller } from "@hotwired/stimulus"

// Mic chart editor. Actors usually keep a mic for several scenes in a
// row, so picking someone for a scene also fills the scenes after it that
// had whoever was there before, up to the next change.
//
//   <form data-controller="chart-fill">
//     <input type="checkbox" data-chart-fill-target="carry" checked>
//     <tr data-chart-fill-target="row">
//       <td><select data-action="focus->chart-fill#remember change->chart-fill#fill">
export default class extends Controller {
  static targets = ["carry", "row"]

  remember(event) {
    event.target.dataset.was = event.target.value
  }

  fill(event) {
    const select = event.target
    const was = select.dataset.was ?? ""
    select.dataset.was = select.value
    if (this.hasCarryTarget && !this.carryTarget.checked) return

    const row = this.rowTargets.find((r) => r.contains(select))
    if (!row) return
    const cells = [...row.querySelectorAll("select")]
    for (const next of cells.slice(cells.indexOf(select) + 1)) {
      if (next.value !== was) break
      next.value = select.value
      next.dataset.was = select.value
      next.classList.add("is-filled")
      setTimeout(() => next.classList.remove("is-filled"), 800)
    }
  }
}
