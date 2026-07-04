package app.vconnect.tunnel

interface JoinController : AutoCloseable {
    fun start()
    override fun close()
}
