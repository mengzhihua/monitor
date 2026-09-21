package dev.monitor.server

import android.app.Notification
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import android.util.Log
import java.io.File
import java.util.ArrayDeque
import java.util.concurrent.atomic.AtomicBoolean

/**
 * Foreground service that runs the Go `monitord` binary as a child process.
 * The binary ships as jniLibs/<abi>/libmonitord.so so the installer extracts
 * it to nativeLibraryDir, the only app-writable-free location that allows exec.
 */
class MonitordService : Service() {
    companion object {
        const val CHANNEL_ID = "monitord"
        const val ACTION_START = "dev.monitor.server.START"
        const val ACTION_STOP = "dev.monitor.server.STOP"
        const val ACTION_STATUS = "dev.monitor.server.STATUS"
        private const val TAG = "monitord"
        private const val NOTIFICATION_ID = 1

        @Volatile var running = false
        @Volatile var port = 0
        val logLines = ArrayDeque<String>()

        fun start(ctx: Context) {
            val i = Intent(ctx, MonitordService::class.java).setAction(ACTION_START)
            ctx.startForegroundService(i)
        }

        fun stop(ctx: Context) {
            ctx.startService(Intent(ctx, MonitordService::class.java).setAction(ACTION_STOP))
        }

        fun binary(ctx: Context): File = File(ctx.applicationInfo.nativeLibraryDir, "libmonitord.so")

        private fun appendLog(line: String) {
            synchronized(logLines) {
                if (logLines.size >= 200) logLines.removeFirst()
                logLines.addLast(line)
            }
        }
    }

    private var process: Process? = null
    private val stopping = AtomicBoolean(false)

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> {
                stopping.set(true)
                process?.destroy()
                stopSelf()
                return START_NOT_STICKY
            }
            else -> {
                if (process == null) launch()
                return START_STICKY
            }
        }
    }

    private fun launch() {
        val settings = Settings(this)
        val bin = binary(this)
        if (!bin.canExecute()) {
            appendLog("binary not found or not executable: $bin")
            stopSelf()
            return
        }
        val dataDir = File(filesDir, "data").apply { mkdirs() }
        val cfg = File(filesDir, "monitor.yaml")
        cfg.writeText(settings.toYaml(dataDir.absolutePath))

        startForegroundCompat(settings.port)
        stopping.set(false)
        val pb = ProcessBuilder(
            bin.absolutePath,
            "-config", cfg.absolutePath,
            "-log-level", "info",
        ).redirectErrorStream(true).directory(filesDir)
        pb.environment()["HOME"] = filesDir.absolutePath
        pb.environment()["TMPDIR"] = cacheDir.absolutePath
        val p = pb.start()
        process = p
        running = true
        port = settings.port
        sendBroadcast(Intent(ACTION_STATUS).setPackage(packageName))
        Thread {
            p.inputStream.bufferedReader().useLines { seq ->
                seq.forEach { line ->
                    appendLog(line)
                    Log.i(TAG, line)
                }
            }
            val code = p.waitFor()
            appendLog("monitord exited with code $code")
            running = false
            process = null
            sendBroadcast(Intent(ACTION_STATUS).setPackage(packageName))
            if (!stopping.get()) {
                // crashed: let the system restart us (START_STICKY)
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
            }
        }.start()
    }

    private fun startForegroundCompat(port: Int) {
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        val n: Notification = Notification.Builder(this, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_stat_monitor)
            .setContentTitle(getString(R.string.notification_title))
            .setContentText(getString(R.string.status_running, port))
            .setContentIntent(open)
            .setOngoing(true)
            .build()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            startForeground(NOTIFICATION_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
        } else {
            startForeground(NOTIFICATION_ID, n)
        }
    }

    override fun onDestroy() {
        stopping.set(true)
        process?.destroy()
        process = null
        running = false
        sendBroadcast(Intent(ACTION_STATUS).setPackage(packageName))
        super.onDestroy()
    }
}
