package bypass.whitelist.ui

import android.annotation.SuppressLint
import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.webkit.CookieManager
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.TextView
import android.widget.Toast
import androidx.activity.OnBackPressedCallback
import androidx.fragment.app.Fragment
import bypass.whitelist.R
import bypass.whitelist.tunnel.CallPlatform
import bypass.whitelist.tunnel.ServiceCookieStore
import bypass.whitelist.util.DESKTOP_USER_AGENT
import bypass.whitelist.util.Prefs

class YandexAccountFragment : Fragment() {
    private lateinit var webView: WebView
    private lateinit var status: TextView
    private val userId by lazy { Prefs.serviceUserId }

    override fun onCreateView(
        inflater: LayoutInflater,
        container: ViewGroup?,
        savedInstanceState: Bundle?,
    ): View = inflater.inflate(R.layout.fragment_yandex_account, container, false)

    @SuppressLint("SetJavaScriptEnabled")
    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        status = view.findViewById(R.id.accountStatus)
        webView = view.findViewById(R.id.accountWebView)
        val cookieManager = CookieManager.getInstance()
        cookieManager.setAcceptCookie(true)
        cookieManager.setAcceptThirdPartyCookies(webView, true)
        webView.settings.javaScriptEnabled = true
        webView.settings.domStorageEnabled = true
        webView.settings.userAgentString = DESKTOP_USER_AGENT
        webView.webViewClient = WebViewClient()

        view.findViewById<View>(R.id.backButton).setOnClickListener { popSelf() }
        view.findViewById<View>(R.id.saveButton).setOnClickListener { saveCookies(cookieManager) }
        view.findViewById<View>(R.id.forgetButton).setOnClickListener { forgetCookies(cookieManager) }
        requireActivity().onBackPressedDispatcher.addCallback(viewLifecycleOwner, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                isEnabled = false
                popSelf()
            }
        })
        updateStatus()
        webView.loadUrl(LOGIN_URL)
    }

    private fun saveCookies(cookieManager: CookieManager) {
        val headers = ServiceCookieStore.yandexUrls().mapNotNull(cookieManager::getCookie)
        runCatching {
            ServiceCookieStore.saveCookieHeaders(requireContext(), userId, CallPlatform.TELEMOST, headers)
        }.onSuccess { count ->
            cookieManager.flush()
            updateStatus()
            Toast.makeText(requireContext(), getString(R.string.yandex_account_saved, count), Toast.LENGTH_SHORT).show()
        }.onFailure {
            Toast.makeText(requireContext(), R.string.yandex_account_not_signed_in, Toast.LENGTH_SHORT).show()
        }
    }

    private fun forgetCookies(cookieManager: CookieManager) {
        ServiceCookieStore.delete(requireContext(), userId, CallPlatform.TELEMOST)
        cookieManager.removeAllCookies { removed ->
            if (removed) cookieManager.flush()
            webView.post { webView.loadUrl(LOGIN_URL) }
        }
        updateStatus()
    }

    private fun updateStatus() {
        status.setText(
            if (ServiceCookieStore.hasCookies(requireContext(), userId, CallPlatform.TELEMOST)) {
                R.string.yandex_account_ready
            } else {
                R.string.yandex_account_required
            }
        )
    }

    private fun popSelf() {
        (activity as? MainActivityHost)?.popSubPage()
    }

    override fun onDestroyView() {
        webView.stopLoading()
        webView.webViewClient = WebViewClient()
        webView.destroy()
        super.onDestroyView()
    }

    private companion object {
        const val LOGIN_URL = "https://passport.yandex.ru/auth?retpath=https%3A%2F%2Ftelemost.yandex.ru%2F"
    }
}
