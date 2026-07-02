package bypass.whitelist.tunnel

import bypass.whitelist.util.Prefs
import org.json.JSONObject

data class EgressProfileStatus(
    val id: String,
    val isDefault: Boolean,
    val available: Boolean?,
    val latencyMs: Long?,
    val error: String?,
)

object EgressDiscovery {
    private const val LIST_MARKER = "EGRESS_LIST:"
    private const val PROBE_MARKER = "EGRESS_PROBE:"

    fun consume(line: String): Boolean {
        val listAt = line.indexOf(LIST_MARKER)
        if (listAt >= 0) {
            val payload = line.substring(listAt + LIST_MARKER.length)
            return try {
                JSONObject(payload).getJSONArray("egresses")
                Prefs.discoveredEgressList = payload
                Prefs.discoveredEgressProbes = "{}"
                true
            } catch (_: Exception) {
                false
            }
        }
        val probeAt = line.indexOf(PROBE_MARKER)
        if (probeAt >= 0) {
            return try {
                val result = JSONObject(line.substring(probeAt + PROBE_MARKER.length))
                val probes = JSONObject(Prefs.discoveredEgressProbes)
                probes.put(result.getString("id"), result)
                Prefs.discoveredEgressProbes = probes.toString()
                true
            } catch (_: Exception) {
                false
            }
        }
        return false
    }

    fun ids(): List<String> = snapshot().map { it.id }

    fun snapshot(): List<EgressProfileStatus> = try {
        val items = JSONObject(Prefs.discoveredEgressList).getJSONArray("egresses")
        val probes = try { JSONObject(Prefs.discoveredEgressProbes) } catch (_: Exception) { JSONObject() }
        List(items.length()) { index ->
            val item = items.getJSONObject(index)
            val id = item.getString("id")
            val probe = probes.optJSONObject(id)
            EgressProfileStatus(
                id = id,
                isDefault = item.optBoolean("isDefault"),
                available = probe?.optBoolean("available"),
                latencyMs = probe?.takeIf { it.has("latencyMs") }?.optLong("latencyMs"),
                error = probe?.optString("error")?.takeIf { it.isNotBlank() },
            )
        }
    } catch (_: Exception) {
        emptyList()
    }

    fun summary(): String {
        val items = snapshot()
        if (items.isEmpty()) return "Available profiles will appear after connection"
        return items.joinToString(" | ") { item ->
            when {
                item.available == null -> "${item.id}: ..."
                item.available -> "${item.id}: ${item.latencyMs ?: 0} ms"
                else -> "${item.id}: offline"
            }
        }
    }
}
