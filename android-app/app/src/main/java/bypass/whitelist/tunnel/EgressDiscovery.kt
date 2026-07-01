package bypass.whitelist.tunnel

import bypass.whitelist.util.Prefs
import org.json.JSONObject

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

    fun ids(): List<String> = try {
        val items = JSONObject(Prefs.discoveredEgressList).getJSONArray("egresses")
        List(items.length()) { index -> items.getJSONObject(index).getString("id") }
    } catch (_: Exception) {
        emptyList()
    }

    fun summary(): String {
        val ids = ids()
        if (ids.isEmpty()) return "Available profiles will appear after connection"
        val probes = try { JSONObject(Prefs.discoveredEgressProbes) } catch (_: Exception) { JSONObject() }
        return ids.joinToString(" · ") { id ->
            val probe = probes.optJSONObject(id)
            when {
                probe == null -> "$id: …"
                probe.optBoolean("available") -> "$id: ${probe.optLong("latencyMs")} ms"
                else -> "$id: offline"
            }
        }
    }
}
