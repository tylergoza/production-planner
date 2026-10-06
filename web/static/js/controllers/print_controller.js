import { Controller } from "@hotwired/stimulus"

// Opens the browser's print dialog.
//   <button data-controller="print" data-action="print#print">Print</button>
export default class extends Controller {
  print() {
    window.print()
  }
}
