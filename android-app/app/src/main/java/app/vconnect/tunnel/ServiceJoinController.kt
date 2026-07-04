package app.vconnect.tunnel

import android.content.Context
import android.os.Handler
import android.os.Looper
import app.vconnect.ui.JoinFragmentHost
import app.vconnect.util.Prefs
import org.json.JSONObject
import java.io.File
import java.util.UUID
import java.util.concurrent.atomic.AtomicBoolean

class ServiceJoinController(
    context: Context,
    private val host: JoinFragmentHost,
    private val destination: CallConfig,
    private val discoveryOnly: Boolean = false,
    private val onEgressList: ((List<EgressProfileStatus>) -> Unit)? = null,
) : JoinController {

    private val mainHandler = Handler(Looper.getMainLooper())
    private val switched = AtomicBoolean(false)
    private val manualSessionRequested = AtomicBoolean(false)
    private val requestId = UUID.randomUUID().toString()
    private val userId = Prefs.serviceUserId
    private val workPlatform = destination.workPlatform ?: CallPlatform.TELEMOST
    private val nativeLibDir = context.applicationInfo.nativeLibraryDir
    private var workController: HeadlessJoinController? = null
    private val serviceController = HeadlessRelayController(
        nativeLibDir = context.applicationInfo.nativeLibraryDir,
        relayMode = "${destination.platform.id}-headless-joiner",
        onLog = { host.appendLog(it) },
        onStatus = ::handleServiceStatus,
        serviceControl = ServiceControlConfig(
            userId = userId,
            cookieFile = ServiceCookieStore.cookieFile(context, userId, workPlatform).takeIf(File::isFile)?.absolutePath,
            cookiePlatform = workPlatform,
            workPlatform = workPlatform,
            requestId = requestId,
            egressId = destination.egressId,
            tunnelMode = destination.effectiveTunnelMode(Prefs.tunnelMode),
            discoveryOnly = discoveryOnly,
        ),
        onServiceSessionReady = ::switchToWorkSession,
        onEgressDiscoveryChanged = ::handleEgressDiscovery,
    )

    override fun start() {
        host.onJoinStatusText("Connecting to service call")
        serviceController.start()
    }

    private fun handleServiceStatus(status: VpnStatus) {
        when (status) {
            VpnStatus.STARTING -> serviceController.sendJoinParams(buildServiceJoinParams().toString())
            VpnStatus.TUNNEL_ACTIVE -> host.onJoinStatusText(if (discoveryOnly) "Loading server list" else "Requesting work call")
            else -> host.onJoinStatus(status)
        }
    }

    private fun handleEgressDiscovery(items: List<EgressProfileStatus>) {
        if (!discoveryOnly || items.isEmpty()) return
        mainHandler.post {
            onEgressList?.invoke(items)
        }
    }

    private fun switchToWorkSession(session: ServiceSessionReady) {
        if (discoveryOnly && !manualSessionRequested.get()) return
        if (session.requestId != requestId || !switched.compareAndSet(false, true)) return
        mainHandler.post {
            serviceController.stop()
            host.onJoinStatusText("Connecting to selected server")
            workController = HeadlessJoinController(
                nativeLibraryDir = nativeLibDir,
                host = host,
                platform = workPlatform,
                url = session.joinLink,
                tunnelMode = destination.effectiveTunnelMode(Prefs.tunnelMode),
            ).also { it.start() }
        }
    }

    fun requestWorkSession(egressId: String) {
        if (!discoveryOnly || switched.get()) return
        manualSessionRequested.set(true)
        host.onJoinStatusText("Requesting work call")
        serviceController.requestServiceSession(egressId)
    }

    private fun buildServiceJoinParams(): JSONObject = JSONObject().apply {
        put("displayName", Prefs.autofillName)
        put("vp8Fps", Prefs.activeVp8Fps)
        put("vp8Batch", Prefs.activeVp8Batch)
        put("dualTrack", Prefs.activeDualTrack)
        when (destination.platform) {
            CallPlatform.TELEMOST -> put("joinLink", destination.url)
            CallPlatform.WBSTREAM -> {
                put("roomId", CallPlatform.extractRoomId(destination.url))
                put("tunnelMode", TunnelMode.VIDEO.relayArg)
            }
            CallPlatform.DION -> put("roomId", CallPlatform.extractRoomId(destination.url))
            CallPlatform.VK -> error("VK service calls require interactive captcha support")
        }
    }

    override fun close() {
        mainHandler.removeCallbacksAndMessages(null)
        serviceController.stop()
        workController?.close()
        workController = null
    }

}
