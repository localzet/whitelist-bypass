package bypass.whitelist.tunnel

import android.content.Context
import android.system.Os
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.io.FileOutputStream

object ServiceCookieStore {
    private val yandexCookieUrls = listOf(
        "https://yandex.ru/",
        "https://passport.yandex.ru/",
        "https://telemost.yandex.ru/",
        "https://telemost.ya.ru/",
    )

    fun yandexUrls(): List<String> = yandexCookieUrls

    fun hasCookies(context: Context, userId: String, platform: CallPlatform): Boolean =
        cookieFile(context, userId, platform).isFile

    fun saveCookieHeaders(
        context: Context,
        userId: String,
        platform: CallPlatform,
        headers: Iterable<String>,
    ): Int {
        val cookies = parseCookieHeaders(headers)

        val payload = JSONArray().apply {
            cookies.forEach { (name, value) ->
                put(JSONObject().put("name", name).put("value", value))
            }
        }.toString()
        val target = cookieFile(context, userId, platform)
        check(target.parentFile?.mkdirs() != false) { "Cannot create cookie directory" }
        val temporary = File(target.parentFile, ".${target.name}.tmp")
        try {
            FileOutputStream(temporary).use { output ->
                output.write(payload.toByteArray(Charsets.UTF_8))
                output.fd.sync()
            }
            check(temporary.setReadable(false, false) && temporary.setReadable(true, true)) {
                "Cannot protect cookie file"
            }
            check(temporary.setWritable(false, false) && temporary.setWritable(true, true)) {
                "Cannot protect cookie file"
            }
            Os.rename(temporary.absolutePath, target.absolutePath)
        } finally {
            temporary.delete()
        }
        return cookies.size
    }

    internal fun parseCookieHeaders(headers: Iterable<String>): Map<String, String> {
        val cookies = linkedMapOf<String, String>()
        headers.forEach { header ->
            header.split(';').forEach { part ->
                val separator = part.indexOf('=')
                if (separator > 0) {
                    val name = part.substring(0, separator).trim()
                    val value = part.substring(separator + 1).trim()
                    if (name.isNotEmpty() && value.isNotEmpty()) cookies[name] = value
                }
            }
        }
        require(!cookies[YANDEX_AUTH_COOKIE].isNullOrEmpty()) { "Yandex session cookie not found" }
        return cookies
    }

    fun delete(context: Context, userId: String, platform: CallPlatform): Boolean {
        val file = cookieFile(context, userId, platform)
        return !file.exists() || file.delete()
    }

    fun cookieFile(context: Context, userId: String, platform: CallPlatform): File =
        File(File(File(context.filesDir, "service-cookies"), userId), "${platform.id}.json")

    private const val YANDEX_AUTH_COOKIE = "Session_id"
}
