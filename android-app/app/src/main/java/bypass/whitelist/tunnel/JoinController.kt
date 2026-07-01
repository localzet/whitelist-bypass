package bypass.whitelist.tunnel

interface JoinController : AutoCloseable {
    fun start()
    override fun close()
}
