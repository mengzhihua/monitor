// Android shell for the monitord server: a foreground service that runs the
// statically linked Go binary shipped as a native library (jniLibs/<abi>/libmonitord.so).
tasks.register<Delete>("clean") {
    delete(rootProject.layout.buildDirectory)
}
