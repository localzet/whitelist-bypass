package bypass.whitelist.tunnel

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test

class ServiceCookieStoreTest {
    @Test
    fun parseCookieHeadersPreservesValuesAndUsesLatestOrigin() {
        val cookies = ServiceCookieStore.parseCookieHeaders(
            listOf(
                "Session_id=first; yandexuid=123",
                "Session_id=second==; other=value",
            )
        )

        assertEquals("second==", cookies["Session_id"])
        assertEquals("123", cookies["yandexuid"])
        assertEquals("value", cookies["other"])
    }

    @Test
    fun parseCookieHeadersRejectsAnonymousCookies() {
        assertThrows(IllegalArgumentException::class.java) {
            ServiceCookieStore.parseCookieHeaders(listOf("yandexuid=123"))
        }
    }
}
