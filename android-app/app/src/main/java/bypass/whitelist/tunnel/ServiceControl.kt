package bypass.whitelist.tunnel

import org.json.JSONObject

data class ServiceControlConfig(
    val userId: String,
    val cookieFile: String?,
    val cookiePlatform: CallPlatform,
    val workPlatform: CallPlatform,
    val requestId: String,
    val egressId: String?,
    val tunnelMode: TunnelMode,
) {
    init {
        require(userId.isNotBlank()) { "Service user ID is required" }
        require(requestId.isNotBlank()) { "Service request ID is required" }
    }

    fun toProcessArgs(): List<String> = buildList {
        add("--service-control")
        addAll(listOf("--service-user-id", userId))
        addAll(listOf("--service-cookie-platform", cookiePlatform.id))
        addAll(listOf("--service-work-platform", workPlatform.id))
        addAll(listOf("--service-request-id", requestId))
        addAll(listOf("--service-tunnel-mode", tunnelMode.relayArg))
        cookieFile?.takeIf { it.isNotBlank() }?.let { addAll(listOf("--service-cookie-file", it)) }
        egressId?.takeIf { it.isNotBlank() }?.let { addAll(listOf("--egress-id", it)) }
    }
}

data class ServiceSessionReady(
    val requestId: String,
    val sessionId: String,
    val joinLink: String,
    val egressId: String,
    val ttlSeconds: Long,
) {
    companion object {
        fun fromJson(value: JSONObject): ServiceSessionReady = ServiceSessionReady(
            requestId = value.getString("requestId"),
            sessionId = value.getString("sessionId"),
            joinLink = value.getString("joinLink"),
            egressId = value.getString("egressId"),
            ttlSeconds = value.getLong("ttlSeconds"),
        )
    }
}
