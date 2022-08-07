#get arch of system
arch=`uname -m`
#get os of system
os=`uname -s`
#if linux
if [ $os = "Linux" ]; then
    ln -sf $PWD/build/linux/$arch/server /usr/local/bin/ferment-upload
    echo "ferment-upload is now available at /usr/local/bin/ferment-upload"
    exit 0
fi
ln -sf $PWD/build/macos/universal/server /usr/local/bin/ferment-upload
echo "ferment-upload is now available at /usr/local/bin/ferment-upload"