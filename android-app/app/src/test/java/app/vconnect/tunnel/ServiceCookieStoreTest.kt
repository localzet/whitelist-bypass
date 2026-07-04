package app.vconnect.tunnel

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test

class ServiceCookieStoreTest {
    @Test
    fun parseCookieHeadersPreservesValuesAndUsesLatestOrigin() {
        val cookies = ServiceCookieStore.parseCookieHeaders(
            listOf(
                "Session_id=first; sessionid2=secondary; yandexuid=123",
                "Session_id=second==; other=value",
            )
        )

        assertEquals("second==", cookies["Session_id"])
        assertEquals("secondary", cookies["sessionid2"])
        assertEquals("123", cookies["yandexuid"])
        assertEquals("value", cookies["other"])
    }

    @Test
    fun parseCookieHeadersRejectsAnonymousCookies() {
        assertThrows(IllegalArgumentException::class.java) {
            ServiceCookieStore.parseCookieHeaders(listOf("yandexuid=123"))
        }
    }

    @Test
    fun parseCookieHeadersRejectsPartialAuthCookies() {
        assertThrows(IllegalArgumentException::class.java) {
            ServiceCookieStore.parseCookieHeaders(listOf("Session_id=primary; yandexuid=123"))
        }
    }
}
