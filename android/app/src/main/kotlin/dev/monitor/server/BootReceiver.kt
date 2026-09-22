package dev.monitor.server

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.os.Build
import android.util.Log

class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action == Intent.ACTION_BOOT_COMPLETED && Settings(context).startOnBoot) {
            // Android 15 forbids dataSync foreground services from BOOT_COMPLETED.
            if (Build.VERSION.SDK_INT >= 35) {
                Log.i("monitord", "Open Monitor to start monitoring after boot on Android 15+")
                return
            }
            try { MonitordService.start(context) } catch (e: RuntimeException) {
                Log.w("monitord", "Background start denied; open Monitor to start", e)
            }
        }
    }
}
