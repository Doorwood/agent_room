const {defineConfig}=require('@playwright/test');
module.exports=defineConfig({testDir:'tests/browser',workers:1,timeout:30000,use:{browserName:'chromium',headless:true,viewport:{width:1280,height:900}},reporter:'list'});
