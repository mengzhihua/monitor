package dev.monitor.server

import android.Manifest
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.TextView
import android.view.WindowManager
import java.io.File
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.core.app.ActivityCompat
import androidx.core.content.ContextCompat

class MainActivity : AppCompatActivity() {
    private lateinit var settings: Settings
    private lateinit var status: TextView
    private lateinit var toggle: Button
    private lateinit var log: TextView
    private val handler = Handler(Looper.getMainLooper())
    private val tick = object : Runnable {
        override fun run() {
            render()
            handler.postDelayed(this, 2000)
        }
    }
    private val statusReceiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) = render()
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        settings = Settings(this)
        status = findViewById(R.id.status)
        toggle = findViewById(R.id.toggle)
        log = findViewById(R.id.log)
        val port = findViewById<EditText>(R.id.port)
        val hubUrl = findViewById<EditText>(R.id.hubUrl)
        val apiKey = findViewById<EditText>(R.id.apiKey)
        val boot = findViewById<CheckBox>(R.id.boot)

        port.setText(settings.port.toString())
        hubUrl.setText(settings.hubUrl)
        apiKey.setText(settings.apiKey)
        boot.isChecked = settings.startOnBoot

        findViewById<Button>(R.id.save).setOnClickListener {
            settings.port = port.text.toString().toIntOrNull()?.coerceIn(1024, 65535) ?: 19999
            settings.hubUrl = hubUrl.text.toString()
            settings.apiKey = apiKey.text.toString()
            settings.startOnBoot = boot.isChecked
            port.setText(settings.port.toString())
        }
        toggle.setOnClickListener {
            if (MonitordService.running) MonitordService.stop(this) else startService()
        }
        findViewById<Button>(R.id.open).setOnClickListener {
            startActivity(Intent(Intent.ACTION_VIEW, Uri.parse("http://127.0.0.1:${settings.port}/")))
        }
        findViewById<Button>(R.id.password).setOnClickListener {
            val password = runCatching { File(filesDir, "data/web-password").readText().trim() }.getOrNull()
            val dialog = AlertDialog.Builder(this)
                .setTitle(R.string.login_password)
                .setMessage(password ?: getString(R.string.password_not_ready))
                .setPositiveButton(android.R.string.ok, null)
                .create()
            dialog.window?.addFlags(WindowManager.LayoutParams.FLAG_SECURE)
            dialog.show()
            dialog.findViewById<TextView>(android.R.id.message)?.setTextIsSelectable(true)
        }
    }

    private fun startService() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            ActivityCompat.requestPermissions(this, arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        }
        MonitordService.start(this)
    }

    override fun onStart() {
        super.onStart()
        ContextCompat.registerReceiver(
            this, statusReceiver, IntentFilter(MonitordService.ACTION_STATUS),
            ContextCompat.RECEIVER_NOT_EXPORTED,
        )
        handler.post(tick)
    }

    override fun onStop() {
        handler.removeCallbacks(tick)
        unregisterReceiver(statusReceiver)
        super.onStop()
    }

    private fun render() {
        val running = MonitordService.running
        status.text = if (running) getString(R.string.status_running, MonitordService.port) else getString(R.string.status_stopped)
        toggle.text = getString(if (running) R.string.stop else R.string.start)
        log.text = synchronized(MonitordService.logLines) { MonitordService.logLines.joinToString("\n") }
    }
}
