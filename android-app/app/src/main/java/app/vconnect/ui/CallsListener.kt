package app.vconnect.ui

import app.vconnect.tunnel.CallConfig

interface CallsListener {
    fun onDestinationSelected(config: CallConfig)
    fun onDestinationsChanged()
}
