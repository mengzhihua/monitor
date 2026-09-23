package dev.monitor.server

import android.content.Context
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import java.util.concurrent.TimeUnit

/** Brings the foreground service back after the process is reclaimed. */
class ReviveWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
    override suspend fun doWork(): Result {
        if (!Settings(applicationContext).startOnBoot) return Result.success()
        return try {
            MonitordService.start(applicationContext)
            Result.success()
        } catch (_: RuntimeException) {
            Result.retry()
        }
    }

    companion object {
        private const val NAME = "monitord-revive"

        fun schedule(ctx: Context) {
            val req = PeriodicWorkRequestBuilder<ReviveWorker>(15, TimeUnit.MINUTES).build()
            WorkManager.getInstance(ctx).enqueueUniquePeriodicWork(
                NAME, ExistingPeriodicWorkPolicy.UPDATE, req,
            )
        }
    }
}
