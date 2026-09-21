plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

val monitorVersion: String = (project.findProperty("monitorVersion") as String?) ?: "0.0.0-dev"
val monitorVersionCode: Int = (project.findProperty("monitorVersionCode") as String?)?.toIntOrNull() ?: 1

android {
    namespace = "dev.monitor.server"
    compileSdk = 35

    defaultConfig {
        applicationId = "dev.monitor.server"
        minSdk = 26
        targetSdk = 35
        versionCode = monitorVersionCode
        versionName = monitorVersion
        ndk {
            abiFilters += listOf("arm64-v8a")
        }
    }

    // The Go binary is placed in jniLibs by scripts/build-android-server.sh so
    // the package installer extracts it to nativeLibraryDir, where it is
    // executable. Do not compress or leave it inside the APK.
    packaging {
        jniLibs.useLegacyPackaging = true
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            // Unsigned by default; release.yml signs when a keystore secret is present.
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
}

dependencies {
    implementation("androidx.core:core-ktx:1.15.0")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("com.google.android.material:material:1.12.0")
}
