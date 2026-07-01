package bypass.whitelist.ui

import android.content.ClipboardManager
import android.content.Context
import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.ArrayAdapter
import android.widget.AutoCompleteTextView
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import androidx.fragment.app.FragmentManager
import com.google.android.material.bottomsheet.BottomSheetDialogFragment
import bypass.whitelist.R
import bypass.whitelist.tunnel.CallConfig
import bypass.whitelist.tunnel.CallPlatform
import bypass.whitelist.tunnel.EgressDiscovery
import bypass.whitelist.util.Prefs
import com.google.android.material.materialswitch.MaterialSwitch

class AddDestinationSheet : BottomSheetDialogFragment() {

    override fun onCreateView(
        inflater: LayoutInflater,
        container: ViewGroup?,
        savedInstanceState: Bundle?,
    ): View = inflater.inflate(R.layout.sheet_add_destination, container, false)

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        val inputName = view.findViewById<EditText>(R.id.inputName)
        val inputLink = view.findViewById<EditText>(R.id.inputLink)
        val inputEgressId = view.findViewById<AutoCompleteTextView>(R.id.inputEgressId)
        val egressDiscoveryStatus = view.findViewById<TextView>(R.id.egressDiscoveryStatus)
        val serviceControl = view.findViewById<MaterialSwitch>(R.id.inputServiceControl)
        val workPlatformContainer = view.findViewById<LinearLayout>(R.id.workPlatformContainer)
        val inputWorkPlatform = view.findViewById<AutoCompleteTextView>(R.id.inputWorkPlatform)
        val pasteChip = view.findViewById<LinearLayout>(R.id.pasteChip)
        val pasteChipLabel = view.findViewById<TextView>(R.id.pasteChipLabel)
        val buttonCancel = view.findViewById<Button>(R.id.buttonCancel)
        val buttonSave = view.findViewById<Button>(R.id.buttonSave)

        inputEgressId.setAdapter(ArrayAdapter(requireContext(), android.R.layout.simple_dropdown_item_1line, EgressDiscovery.ids()))
        inputEgressId.setOnClickListener { inputEgressId.showDropDown() }
        egressDiscoveryStatus.text = EgressDiscovery.summary()
        val workPlatforms = CallPlatform.entries
        inputWorkPlatform.setAdapter(ArrayAdapter(requireContext(), android.R.layout.simple_dropdown_item_1line, workPlatforms.map { it.name }))
        inputWorkPlatform.setText(CallPlatform.TELEMOST.name, false)
        inputWorkPlatform.setOnClickListener { inputWorkPlatform.showDropDown() }
        serviceControl.setOnCheckedChangeListener { _, checked ->
            workPlatformContainer.visibility = if (checked) View.VISIBLE else View.GONE
        }

        pasteChip.setOnClickListener {
            val clipboard = requireContext().getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
            val clip = clipboard.primaryClip
            val text = clip?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.coerceToText(requireContext())?.toString().orEmpty().trim()
            if (text.isEmpty()) {
                Toast.makeText(requireContext(), R.string.clipboard_empty, Toast.LENGTH_SHORT).show()
                return@setOnClickListener
            }
            inputLink.setText(text)
            if (inputName.text.toString().trim().isEmpty()) {
                inputName.setText(CallConfig.suggestNameFor(text))
            }
            flashChip(pasteChip, pasteChipLabel)
        }

        buttonCancel.setOnClickListener { dismiss() }

        buttonSave.setOnClickListener {
            val link = inputLink.text.toString().trim()
            if (link.isEmpty()) {
                inputLink.requestFocus()
                return@setOnClickListener
            }
            val name = inputName.text.toString().trim().ifEmpty { CallConfig.suggestNameFor(link) }
            val egressId = inputEgressId.text.toString().trim().ifEmpty { null }
            if (serviceControl.isChecked && CallPlatform.fromUrl(link) != CallPlatform.WBSTREAM) {
                inputLink.error = getString(R.string.sheet_service_wb_only)
                inputLink.requestFocus()
                return@setOnClickListener
            }
            val workPlatform = inputWorkPlatform.text.toString().let { value ->
                workPlatforms.firstOrNull { it.name == value }
            }
            if (serviceControl.isChecked && workPlatform == null) {
                inputWorkPlatform.requestFocus()
                return@setOnClickListener
            }
            val config = CallConfig.newWith(name = name, url = link).copy(
                egressId = egressId,
                serviceControl = serviceControl.isChecked,
                workPlatform = workPlatform.takeIf { serviceControl.isChecked },
            )
            Prefs.addDestination(config)
            (parentFragment as? CallsListener)?.onDestinationsChanged()
            (activity as? CallsListener)?.onDestinationsChanged()
            (activity as? CallsListener)?.onDestinationSelected(config)
            dismiss()
        }
    }

    private fun flashChip(chip: LinearLayout, label: TextView) {
        chip.setBackgroundResource(R.drawable.bg_paste_chip_flash)
        label.setTextColor(requireContext().getColor(R.color.accent_emerald))
        chip.postDelayed({
            if (isAdded) {
                chip.setBackgroundResource(R.drawable.bg_paste_chip)
                label.setTextColor(requireContext().getColor(R.color.ink_2))
            }
        }, 320)
    }

    companion object {
        const val TAG = "AddDestinationSheet"

        fun show(manager: FragmentManager) {
            AddDestinationSheet().show(manager, TAG)
        }
    }
}
